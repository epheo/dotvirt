package model

// Modules is GET /api/modules: the parts of the console this cluster serves.
// What the caller may do inside one stays Capabilities' answer.
type Modules struct {
	VMs               bool `json:"vms"`
	Desktops          bool `json:"desktops"`
	Networks          bool `json:"networks"`
	Rebalancing       bool `json:"rebalancing"`
	Routes            bool `json:"routes"`
	ReplicatedStorage bool `json:"replicatedStorage"`
	// OLM and MachineConfig are what this cluster can install, not show:
	// rebalancing installs its operator through OLM (Subscriptions and
	// OperatorGroups), host pressure readings add a MachineConfig.
	OLM           bool `json:"olm"`
	MachineConfig bool `json:"machineConfig"`
}
