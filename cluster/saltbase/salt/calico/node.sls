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

calico-cni:
  file.managed:
    - name: /opt/cni/bin/calico
    - source: https://github.com/projectcalico/calico-cni/releases/download/v1.2.0/calico
    - source_hash: sha256=499d507666300c900596d1a4254e7a4eea900100bc73bd54bc903633afbfbcf4
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
      - file: host-local
      - file: calico-cni-config
      - cmd: calico-node
      - service: kubelet

host-local:
  file.managed:
    - name: /opt/cni/bin/host-local
    - source: https://f001.backblaze.com/file/calico/host-local
    - source_hash: sha256=0eb6324764cd651072d53094808d6d15c4acb5c46a5d55d70edb0395309ee928
    - makedirs: True
    - mode: 744

ip6_tables:
  kmod.present

xt_set:
  kmod.present

{% endif %}