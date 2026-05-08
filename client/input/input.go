package input

// Event types sent from the agent.
const (
	TypeMouseMove = "mouse_move"
	TypeMouseDown = "mouse_down"
	TypeMouseUp   = "mouse_up"
	TypeScroll    = "scroll"
	TypeKeyDown   = "key_down"
	TypeKeyUp     = "key_up"
)

// Event is a decoded input message from the agent.
type Event struct {
	Type string `json:"type"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Btn  string `json:"btn"`
	Dx   int    `json:"dx"`
	Dy   int    `json:"dy"`
	VK   int    `json:"vk"`
	Key  string `json:"key"`
}

// Injector injects input events into the OS.
type Injector interface {
	Inject(e Event) error
}
