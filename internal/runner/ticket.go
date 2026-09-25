package runner

// Ticket is one unit of work handed to the build agent.
type Ticket struct {
	ID      string
	Title   string
	Size    string
	SizedBy string
	Body    string
}

// Tracer is the hardcoded ticket the runner builds.
var Tracer = Ticket{
	ID:      "tracer-1",
	Title:   "feat(surface): add GET /api/version",
	Size:    "S",
	SizedBy: "hardcoded",
	Body:    "Add `GET /api/version`, returning the build's commit sha.",
}

// Text renders the ticket as it appears at the end of the prompt.
func (t Ticket) Text() string {
	return "## " + t.ID + " (size " + t.Size + ")\n\n" + t.Body
}
