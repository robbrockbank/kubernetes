{% if pillar.get('policy_provider', '').lower() == 'calico' %}

calicoctl:
  file.managed:
    - name: /usr/bin/calicoctl
    - source: https://github.com/projectcalico/calico-docker/releases/download/v0.18.0/calicoctl
    - source_hash: sha256=694d2e0d0079980d75bc807fcb6626b18e5994638aa62743d45b906e742a0eed
    - makedirs: True
    - mode: 744

calico-policy:
  file.managed:
    - name: /usr/bin/policy
    - source: https://github.com/projectcalico/k8s-policy/releases/download/v0.1.3/policy
    - source_hash: sha256=def1b53ec0bf3ec2dce9edb7b4252a514ccd6b06c7e738a324e0a3e9ecf12bbe
    - makedirs: True
    - mode: 744

calico-etcd:
  cmd.run:
    - unless: docker ps | grep calico-etcd
    - name: >
               docker run --name calico-etcd -d --restart=always -p 6666:6666
               -v /varetcd:/var/etcd
               gcr.io/google_containers/etcd:2.2.1
               /usr/local/bin/etcd --name calico
               --data-dir /var/etcd/calico-data
               --advertise-client-urls http://{{ grains.id }}:6666
               --listen-client-urls http://0.0.0.0:6666
               --listen-peer-urls http://0.0.0.0:6667
               --initial-advertise-peer-urls http://{{ grains.id }}:6667
               --initial-cluster calico=http://{{ grains.id }}:6667

calico-node:
  cmd.run:
    - name: calicoctl node
    - unless: docker ps | grep calico-node
    - env:
      - ETCD_AUTHORITY: "{{ grains.id }}:6666"
    - require:
      - kmod: ip6_tables
      - kmod: xt_set
      - service: docker
      - file: calicoctl
      - cmd: calico-etcd

calico-policy-agent:
  file.managed:
    - name: /etc/kubernetes/manifests/calico-policy-agent.manifest
    - source: salt://calico/calico-policy-agent.manifest
    - template: jinja
    - user: root
    - group: root
    - mode: 644
    - makedirs: true
    - dir_mode: 755
    - context:
        cpurequest: '20m'
    - require:
      - service: docker
      - service: kubelet
      - cmd: calico-node

ip6_tables:
  kmod.present

xt_set:
  kmod.present

{% endif %}