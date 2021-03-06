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

package validation

import (
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
)

const qnameCharFmt string = "[A-Za-z0-9]"
const qnameExtCharFmt string = "[-A-Za-z0-9_.]"
const qualifiedNameFmt string = "(" + qnameCharFmt + qnameExtCharFmt + "*)?" + qnameCharFmt
const qualifiedNameMaxLength int = 63

var qualifiedNameMaxLengthString = strconv.Itoa(qualifiedNameMaxLength)
var qualifiedNameRegexp = regexp.MustCompile("^" + qualifiedNameFmt + "$")

// IsQualifiedName tests whether the value passed is what Kubernetes calls a
// "qualified name".  This is a format used in various places throughout the
// system.  If the value is not valid, a list of error strings is returned.
// Otherwise an empty list (or nil) is returned.
func IsQualifiedName(value string) []string {
	var errs []string
	parts := strings.Split(value, "/")
	var name string
	switch len(parts) {
	case 1:
		name = parts[0]
	case 2:
		var prefix string
		prefix, name = parts[0], parts[1]
		if len(prefix) == 0 {
			errs = append(errs, "prefix part must be non-empty")
		} else if !IsDNS1123Subdomain(prefix) {
			errs = append(errs, fmt.Sprintf("prefix part must be a DNS subdomain (e.g. 'example.com')"))
		}
	default:
		return append(errs, "must match the regex "+qualifiedNameFmt+" with an optional DNS subdomain prefix and '/' (e.g. 'MyName' or 'example.com/MyName'")
	}

	if len(name) == 0 {
		errs = append(errs, "name part must be non-empty")
	} else if len(name) > qualifiedNameMaxLength {
		errs = append(errs, "name part must be no more than "+qualifiedNameMaxLengthString+" characters")
	}
	if !qualifiedNameRegexp.MatchString(name) {
		errs = append(errs, "name part must match the regex "+qualifiedNameFmt+" (e.g. 'MyName' or 'my.name' or '123-abc')")
	}
	return errs
}

const labelValueFmt string = "(" + qualifiedNameFmt + ")?"
const LabelValueMaxLength int = 63

var labelValueMaxLengthString = strconv.Itoa(LabelValueMaxLength)
var labelValueRegexp = regexp.MustCompile("^" + labelValueFmt + "$")

// IsValidLabelValue tests whether the value passed is a valid label value.  If
// the value is not valid, a list of error strings is returned.  Otherwise an
// empty list (or nil) is returned.
func IsValidLabelValue(value string) []string {
	var errs []string
	if len(value) > LabelValueMaxLength {
		errs = append(errs, "must be no more than "+labelValueMaxLengthString+" characters")
	}
	if !labelValueRegexp.MatchString(value) {
		errs = append(errs, "must match the regex "+labelValueFmt+" (e.g. 'MyValue' or 'my_value' or '12345')")
	}
	return errs
}

const DNS1123LabelFmt string = "[a-z0-9]([-a-z0-9]*[a-z0-9])?"
const DNS1123LabelMaxLength int = 63

var dns1123LabelRegexp = regexp.MustCompile("^" + DNS1123LabelFmt + "$")

// IsDNS1123Label tests for a string that conforms to the definition of a label in
// DNS (RFC 1123).
func IsDNS1123Label(value string) bool {
	return len(value) <= DNS1123LabelMaxLength && dns1123LabelRegexp.MatchString(value)
}

const DNS1123SubdomainFmt string = DNS1123LabelFmt + "(\\." + DNS1123LabelFmt + ")*"
const DNS1123SubdomainMaxLength int = 253

var dns1123SubdomainRegexp = regexp.MustCompile("^" + DNS1123SubdomainFmt + "$")

// IsDNS1123Subdomain tests for a string that conforms to the definition of a
// subdomain in DNS (RFC 1123).
func IsDNS1123Subdomain(value string) bool {
	return len(value) <= DNS1123SubdomainMaxLength && dns1123SubdomainRegexp.MatchString(value)
}

const DNS952LabelFmt string = "[a-z]([-a-z0-9]*[a-z0-9])?"
const DNS952LabelMaxLength int = 24

var dns952LabelRegexp = regexp.MustCompile("^" + DNS952LabelFmt + "$")

// IsDNS952Label tests for a string that conforms to the definition of a label in
// DNS (RFC 952).
func IsDNS952Label(value string) bool {
	return len(value) <= DNS952LabelMaxLength && dns952LabelRegexp.MatchString(value)
}

const CIdentifierFmt string = "[A-Za-z_][A-Za-z0-9_]*"

var cIdentifierRegexp = regexp.MustCompile("^" + CIdentifierFmt + "$")

// IsCIdentifier tests for a string that conforms the definition of an identifier
// in C. This checks the format, but not the length.
func IsCIdentifier(value string) bool {
	return cIdentifierRegexp.MatchString(value)
}

// IsValidPortNum tests that the argument is a valid, non-zero port number.
func IsValidPortNum(port int) bool {
	return 0 < port && port < 65536
}

// Now in libcontainer UID/GID limits is 0 ~ 1<<31 - 1
// TODO: once we have a type for UID/GID we should make these that type.
const (
	minUserID  = 0
	maxUserID  = math.MaxInt32
	minGroupID = 0
	maxGroupID = math.MaxInt32
)

// IsValidGroupId tests that the argument is a valid gids.
func IsValidGroupId(gid int64) bool {
	return minGroupID <= gid && gid <= maxGroupID
}

// IsValidUserId tests that the argument is a valid uids.
func IsValidUserId(uid int64) bool {
	return minUserID <= uid && uid <= maxUserID
}

const doubleHyphensFmt string = ".*(--).*"

var doubleHyphensRegexp = regexp.MustCompile("^" + doubleHyphensFmt + "$")

const IdentifierNoHyphensBeginEndFmt string = "[a-z0-9]([a-z0-9-]*[a-z0-9])*"

var identifierNoHyphensBeginEndRegexp = regexp.MustCompile("^" + IdentifierNoHyphensBeginEndFmt + "$")

const atLeastOneLetterFmt string = ".*[a-z].*"

var atLeastOneLetterRegexp = regexp.MustCompile("^" + atLeastOneLetterFmt + "$")

// IsValidPortName check that the argument is valid syntax. It must be non empty and no more than 15 characters long
// It must contains at least one letter [a-z] and it must contains only [a-z0-9-].
// Hypens ('-') cannot be leading or trailing character of the string and cannot be adjacent to other hyphens.
// Although RFC 6335 allows upper and lower case characters but case is ignored for comparison purposes: (HTTP
// and http denote the same service).
func IsValidPortName(port string) bool {
	if len(port) < 1 || len(port) > 15 {
		return false
	}
	if doubleHyphensRegexp.MatchString(port) {
		return false
	}
	if identifierNoHyphensBeginEndRegexp.MatchString(port) && atLeastOneLetterRegexp.MatchString(port) {
		return true
	}
	return false
}

// IsValidIP tests that the argument is a valid IP address.
func IsValidIP(value string) bool {
	return net.ParseIP(value) != nil
}

const percentFmt string = "[0-9]+%"

var percentRegexp = regexp.MustCompile("^" + percentFmt + "$")

func IsValidPercent(percent string) bool {
	return percentRegexp.MatchString(percent)
}

const HTTPHeaderNameFmt string = "[-A-Za-z0-9]+"

var httpHeaderNameRegexp = regexp.MustCompile("^" + HTTPHeaderNameFmt + "$")

// IsHTTPHeaderName checks that a string conforms to the Go HTTP library's
// definition of a valid header field name (a stricter subset than RFC7230).
func IsHTTPHeaderName(value string) bool {
	return httpHeaderNameRegexp.MatchString(value)
}
