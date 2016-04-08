{% if pillar.get('policy_provider', '').lower() == 'calico' %}

calicoctl:
  file.managed:
    - name: /usr/bin/calicoctl
    - source: https://github.com/projectcalico/calico-docker/releases/download/v0.18.0/calicoctl
    - makedirs: True
    - mode: 744

calico-policy:
  file.managed:
    - name: /usr/bin/policy
    - source: https://github.com/projectcalico/k8s-policy/releases/download/v0.1.3/policy
    - makedirs: True
    - mode: 744

calico-node:
  cmd.run:
    - name: calicoctl node
    - unless: docker ps | grep calico-node
    - env:
      - ETCD_AUTHORITY: "{{ grains.api_servers }}:6666"
    - require:
      - kmod: ip6_tables
      - kmod: xt_set
      - service: docker
      - file: calicoctl
      - cmd: calico-etcd

calico-cni:
  file.managed:
    - name: /opt/cni/bin/calico
    - source: https://github.com/projectcalico/calico-cni/releases/download/v1.2.0/calico
    - makedirs: True
    - mode: 744

calico-cni-config:
  file.managed:
    - name: /etc/cni/net.d/10-calico.conf
    - source: salt://calico/10-calico.conf
    - makedirs: True
    - mode: 644
    - template: jinja

calico-restart-kubelet:
  cmd.run:
    - name: service kubelet restart
    - require:
      - file: calico-cni
      - file: calico-cni-config
      - cmd: calico-node
      - service: kubelet

ip6_tables:
  kmod.present

xt_set:
  kmod.present

{% endif %}