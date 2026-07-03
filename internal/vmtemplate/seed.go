package vmtemplate

import "github.com/epheo/dotvirt/internal/git"

// SeedFiles is the starter library committed to a newly created project repo:
// a full guest-customization example and a minimal one, both deployable with
// defaults alone. The DataSource/instancetype/preference names match what CNV
// ships (and what the New-VM wizard offers); OS_IMAGE_NAMESPACE exists so
// clusters that keep boot images elsewhere can retarget without editing YAML.
func SeedFiles() []git.File {
	return []git.File{
		{Path: Dir + "/fedora-server.yaml", Content: []byte(fedoraServer)},
		{Path: Dir + "/fedora-minimal.yaml", Content: []byte(fedoraMinimal)},
	}
}

const fedoraServer = `apiVersion: template.kubevirt.io/v1beta1
kind: VirtualMachineTemplate
metadata:
  name: fedora-server
  annotations:
    description: Fedora server with cloud-init guest customization (hostname, user, password, SSH key)
spec:
  parameters:
    - name: NAME
      description: Unique VM name
      generate: expression
      from: "fedora-[a-z0-9]{5}"
    - name: HOSTNAME
      description: Guest hostname
      value: fedora
    - name: CLOUD_USER
      description: Cloud-init user account
      value: fedora
    - name: CLOUD_PASSWORD
      description: Password for the cloud-init user
      generate: expression
      from: "[a-zA-Z0-9]{16}"
    - name: SSH_AUTHORIZED_KEY
      description: SSH public key added to the cloud-init user (optional)
    - name: OS_IMAGE_NAMESPACE
      description: Namespace of the fedora boot-image DataSource
      value: openshift-virtualization-os-images
  virtualMachine:
    apiVersion: kubevirt.io/v1
    kind: VirtualMachine
    metadata:
      name: ${NAME}
    spec:
      runStrategy: Halted
      instancetype:
        name: u1.medium
      preference:
        name: fedora
      dataVolumeTemplates:
        - metadata:
            name: ${NAME}-rootdisk
          spec:
            sourceRef:
              kind: DataSource
              name: fedora
              namespace: ${OS_IMAGE_NAMESPACE}
            storage:
              resources:
                requests:
                  storage: 30Gi
      template:
        spec:
          domain:
            devices:
              disks:
                - name: rootdisk
                  disk:
                    bus: virtio
                - name: cloudinitdisk
                  disk:
                    bus: virtio
              interfaces:
                - name: default
                  masquerade: {}
          networks:
            - name: default
              pod: {}
          volumes:
            - name: rootdisk
              dataVolume:
                name: ${NAME}-rootdisk
            - name: cloudinitdisk
              cloudInitNoCloud:
                userData: |
                  #cloud-config
                  hostname: ${HOSTNAME}
                  user: ${CLOUD_USER}
                  password: ${CLOUD_PASSWORD}
                  chpasswd: { expire: false }
                  ssh_authorized_keys:
                    - "${SSH_AUTHORIZED_KEY}"
`

const fedoraMinimal = `apiVersion: template.kubevirt.io/v1beta1
kind: VirtualMachineTemplate
metadata:
  name: fedora-minimal
  annotations:
    description: Minimal Fedora VM — a generated name is the only parameter
spec:
  parameters:
    - name: NAME
      description: Unique VM name
      generate: expression
      from: "fedora-[a-z0-9]{5}"
    - name: OS_IMAGE_NAMESPACE
      description: Namespace of the fedora boot-image DataSource
      value: openshift-virtualization-os-images
  virtualMachine:
    apiVersion: kubevirt.io/v1
    kind: VirtualMachine
    metadata:
      name: ${NAME}
    spec:
      runStrategy: Halted
      instancetype:
        name: u1.small
      preference:
        name: fedora
      dataVolumeTemplates:
        - metadata:
            name: ${NAME}-rootdisk
          spec:
            sourceRef:
              kind: DataSource
              name: fedora
              namespace: ${OS_IMAGE_NAMESPACE}
            storage:
              resources:
                requests:
                  storage: 30Gi
      template:
        spec:
          domain:
            devices:
              disks:
                - name: rootdisk
                  disk:
                    bus: virtio
              interfaces:
                - name: default
                  masquerade: {}
          networks:
            - name: default
              pod: {}
          volumes:
            - name: rootdisk
              dataVolume:
                name: ${NAME}-rootdisk
`
