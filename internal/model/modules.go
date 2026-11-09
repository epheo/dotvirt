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
}
