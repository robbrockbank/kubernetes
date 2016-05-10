/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Based on the network-tester, this implements a network monitor that can
// be used to continuously monitor connectivity between a netmonitor container
// and a set of peer netmonitors (discovered through namespace/service query).
//
// Connectivity between the containers is monitored using a lightweight web
// server (for TCP traffic).  This container tracks when the last message was
// responded to by each peer, _and_ when it last received a request from the
// peer - thus this tracks both inbound and outbound connectivity separately.
//
// The container also serves webserver UI to allow a client to receive the
// connectivity information between the pods.  It serves the following endpoints:
//
// /quit       : to shut down
// /summary    : to see a summary status of connections
// /detailed   : to see the detailed internal state
//
// The internal facing webserver serves up the following:
// /ping
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"k8s.io/kubernetes/pkg/api"
	client "k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/util/sets"
)

// Information about an individual peer message.
type TCPMessage struct {
	// Values in this structure require the State lock to be held before
	// reading or writing.
	NextIndex             int
	LastSuccessIndex      int
	LastSuccessTime       int
	TimeBetweenMessages   []int
	TimeToExpire          int
}

// State tracks the internal connection state of all peers.
type State struct {
	// Hostname is set once and never changed-- it's always safe to read.
	Hostname string

	// The below fields require that lock is held before reading or writing.
	TCPOutbound          map[string]*TCPMessage
	TCPInbound           map[string]*TCPMessage
	LastError            string
	Logs                 []string

	lock sync.Mutex
}

// HTTPPingMessage is the format that (json encoded) requests to the /ping handler should take,
// and is returned directly as the response.
type HTTPPingMessage struct {
	Index    int
	SourceID string
}

// Summary is returned by /summary
type Summary struct {
	TCPNumOutboundConnected   int
	TCPNumOutboundFailed      int
	TCPNumInboundConnected    int
	TCPNumInboundFailed       int
}

var (
	// Runtime flags
	tcpPort       = flag.Int("tcp-port", 8081, "TCP Port number used for peer2peer testing.")
	uiPort        = flag.Int("ui-port", 8080, "TCP Port number used for HTTP requests to query status and reset counts.")
	namespace     = flag.String("namespace", "default", "Namespace containing peer network monitor pods.")
	service       = flag.String("service", "netmonitor", "Service containing peer network monitor pods.")
	numPeers      = flag.Int("num-peers", 0, "Expected number of peer network monitor pods.")
	expireFactor  = flag.Int("expiration-factor", 3, "Factor to multiple time between received messages which is then used to tune the expiration time (chosen as the maximum of the current value and the factored time).")

	// Our one and only state object
	state         State

	// Number of message times to keep track of
	msgHistory    = 3
)

func (t *TCPMessage) isConnected(now int) bool {
	if !t.TimeOfLastSuccess {
		return false
	}

	// We only consider ourselves connected when we have received at least a
	// couple of consecutive messages so that we know what the approximate time
	// between messages is - without that we can't determine what constitutes
	// a fail.
	if !len(t.TimeBetweenMessages) {
		return false
	}

	// Calculate the average time between messages.  We multiply this by our
	// expiration factor to determine when there is no connection.
	timeBetweenMessages := 0
	for i := 0; i < len(t.TimeBetweenMessages); i++ {
		timeBetweenMessages += t.TimeBetweenMessages[i]
	}
	timeBetweenMessages = timeBetweenMessages / len(t.TimeBetweenMessages)

	// Check if the time of last success indicates that the channel is still
	// connected.
	return t.LastSuccessTime +  (timeBetweenMessages * expireFactor) > now
}

// serveSummary returns a JSON dictionary containing a summary of
// counts of successful inbound and outbound peers connections.
// e.g.
//   {
//     "TCPNumInboundFailed": 4,
//     "TCPNumInboundConnected": 10,
//     "TCPNumOutboundFailed": 0,
//     "TCPNumOutboundConnected": 4
//   }
func (s *State) serveSummary(w http.ResponseWriter, r *http.Request) {
	s.lock.Lock()
	defer s.lock.Unlock()

	now := time.Now()
	summary := Summary{
		TCPNumInboundFailed: 0,
		TCPNumInboundConnected: 0,
		TCPNumOutboundFailed: 0,
		TCPNumOutboundConnected: 0,
	}

	for _, v := range s.TCPOutbound {
		if v.isConnected(now) {
			summary.TCPNumOutboundConnected += 1
		} else {
			summary.TCPNumOutboundFailed += 1
		}
	}

	for _, v := range s.TCPInbound {
		if v.isConnected(now) {
			summary.TCPNumInboundConnected += 1
		} else {
			summary.TCPNumInboundFailed += 1
		}
	}

	w.WriteHeader(http.StatusOK)
	b, err := json.MarshalIndent(&summary, "", "\t")
	s.appendErr(err)
	_, err = w.Write(b)
	s.appendErr(err)
}

// serveDetailed writes our json encoded state
func (s *State) serveDetailed(w http.ResponseWriter, r *http.Request) {
	s.lock.Lock()
	defer s.lock.Unlock()
	w.WriteHeader(http.StatusOK)
	b, err := json.MarshalIndent(s, "", "\t")
	s.appendErr(err)
	_, err = w.Write(b)
	s.appendErr(err)
}

// servePing responds to a ping from a peer, and records the peer contact in our
// received state.
func (s *State) servePing(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	s.lock.Lock()
	defer s.lock.Unlock()
	w.WriteHeader(http.StatusOK)
	var msg HTTPPingMessage
	s.appendErr(json.NewDecoder(r.Body).Decode(&msg))
	if msg.SourceID == "" {
		s.appendErr(fmt.Errorf("%v: Got request with no source ID", s.Hostname))
	} else {
		now := time.Now()
		stored := s.TCPInbound[msg.SourceID]
		if msg.Index >= stored.NextIndex {
			if msg.Index == stored.NextIndex {
				// This is a consecutive message so we can record the time between
				// messages.  We use this to adjust our expiration times based
				// on load.
				append(stored.TimeBetweenMessages, now - stored.LastSuccessTime)
				if len(stored.TimeBetweenMessages) > msgHistory {
					stored.TimeBetweenMessages = stored.TimeBetweenMessages[1:]
				}
			}

			// Store the index and the current time.
			stored.LastSuccessTime = now
			stored.LastSuccessIndex = msg.Index

			// Update the next index we expect.
			stored.NextIndex = msg.Index + 1
		}

		// Update the map to store the data for this connection.  This is only actually
		// required when we create a new entry.
		s.TCPInbound[msg.SourceID] = stored
	}

	// Send the original request back as the response.
	s.appendErr(json.NewEncoder(w).Encode(&msg))
}

// appendErr adds err to the list, if err is not nil. s must be locked.
func (s *State) appendErr(err error) {
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
}

// Logf writes to the log message list. s must not be locked.
// s's Log member will drop an old message if it would otherwise
// become longer than 500 messages.
func (s *State) Logf(format string, args ...interface{}) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.Log = append(s.Log, fmt.Sprintf(format, args...))
	if len(s.Log) > 500 {
		s.Log = s.Log[1:]
	}
}

func (s *State) monitorTCPPeer(endpoint string) {
	var index int

	// Obtain the current index for this peer and increment it in our shared data.  We
	// need to hold the lock for this, but only want to hold it for the minimum amount
	// of time.
	func () {
		s.lock.Lock()
		defer s.lock.Unlock()

		data := s.TCPOutbound[endpoint]
		index = data.NextIndex
		data.NextIndex += 1
		s.TCPOutbound[endpoint] = data
	}()

	// Send the HTTP ping request.
	s.Logf("Attempting to contact %s", endpoint)
	body, err := json.Marshal(&HTTPPingMessage{
		Index:    index,
		SourceID: s.Hostname,
	})
	if err != nil {
		log.Fatalf("json marshal error: %v", err)
	}
	resp, err := http.Post(endpoint + "/ping", "application/json", bytes.NewReader(body))
	if err != nil {
		state.Logf("Warning: unable to contact the endpoint %q: %v", e, err)
		return
	}
	defer resp.Body.Close()

	// Read the response.
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		state.Logf("Warning: unable to read response from '%v': '%v'", e, err)
		return
	}
	var response HTTPPingMessage
	err = json.Unmarshal(body, &response)
	if err != nil {
		state.Logf("Warning: unable to unmarshal response (%v) from '%v': '%v'", string(body), e, err)
		return
	}

	func() {
		s.lock.Lock()
		defer s.lock.Unlock()

		now := time.Now()
		stored := s.TCPOutbound[endpoint]
		if index == stored.LastSuccessIndex + 1 {
			// This is a consecutive message so we can record the time between
			// messages.  We use this to adjust our expiration times based
			// on load.
			append(stored.TimeBetweenMessages, now - stored.LastSuccessTime)
			if len(stored.TimeBetweenMessages) > msgHistory {
				stored.TimeBetweenMessages = stored.TimeBetweenMessages[1:]
			}
		}
		if index > stored.LastSuccessIndex {
			stored.LastSuccessIndex = index
			stored.LastSuccessTime = now
		}
	}()
}

func main() {
	flag.Parse()

	if *service == "" {
		log.Fatal("Must provide -service flag.")
	}

	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("Error getting hostname: %v", err)
	}

	if *delayShutdown > 0 {
		termCh := make(chan os.Signal)
		signal.Notify(termCh, syscall.SIGTERM)
		go func() {
			<-termCh
			log.Printf("Sleeping %d seconds before exit ...", *delayShutdown)
			time.Sleep(time.Duration(*delayShutdown) * time.Second)
			os.Exit(0)
		}()
	}

	state := State{
		Hostname:             hostname,
		TCPOutbound:          map[string]*TCPMessage{},
		TCPInbound:           map[string]*TCPMessage{},
	}

	go monitorPeers(&state)

	http.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		os.Exit(0)
	})
	http.HandleFunc("/summary", state.serveSummary)
	http.HandleFunc("/detailed", state.serveDetailed)
	http.HandleFunc("/ping", state.servePing)

	// Start up the server on the required ports - by default the inter-pod communication
	// is on a different port to the UX.  Both can be handled by the same default
	// handler though.
	go log.Fatal(http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", *tcpPort), nil))
	if uiPort != tcpPort {
		go log.Fatal(http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", *uiPort), nil))
	}

	select {}
}

// Find all sibling pods in the service and post to their /write handler.
func monitorPeers(state *State) {
	client, err := client.NewInCluster()
	if err != nil {
		log.Fatalf("Unable to create client; error: %v\n", err)
	}
	// Double check that that worked by getting the server version.
	if v, err := client.Discovery().ServerVersion(); err != nil {
		log.Fatalf("Unable to get server version: %v\n", err)
	} else {
		log.Printf("Server version: %#v\n", v)
	}

	// Loop until we at least find the correct number of endpoints.
	for {
		tcp_eps := getEndpoints(client)
		if tcp_eps.Len() >= *numPeers {
			break
		}
		state.Logf("%v/%v has %v TCP endpoints (%v), which is fewer than %v as expected. Waiting for all endpoints to come up.", *namespace, *service, len(tcp_eps), tcp_eps.List(), *numPeers)
	}

	// Do this repeatedly, in case there's some propagation delay with getting
	// newly started pods into the endpoints list.
	for {
		tcp_eps := getEndpoints(client)
		for ep := range tcp_eps {
			state.monitorTCPPeer(ep)
		}
		time.Sleep(5 * time.Second)
	}
}

// getEndpoints returns the endpoints as set of String:
// -  TCP endpoints:  "http://{ip}:{port}"
func getEndpoints(client *client.Client) sets.String {
	endpoints, err := client.Endpoints(*namespace).Get(*service)
	tcp_eps := sets.String{}
	if err != nil {
		state.Logf("Unable to read the endpoints for %v/%v: %v.", *namespace, *service, err)
		return eps
	}
	for _, ss := range endpoints.Subsets {
		for _, a := range ss.Addresses {
			for _, p := range ss.Ports {
				if p.Protocol == api.ProtocolTCP {
					eps.Insert(fmt.Sprintf("http://%s:%d", a.IP, p.Port))
				}
			}
		}
	}
	return tcp_eps
}
