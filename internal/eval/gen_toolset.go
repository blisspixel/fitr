package eval

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
)

// maxToolSet bounds a generated tool list. Task files are user input and the
// distractor count indexes a generated pool, so an unbounded value would be an
// allocation read from a config file.
const maxToolSet = 64

// distractorTools are plausible neighbours, not noise. A model choosing among
// obviously irrelevant tools is not doing the task an agent harness asks of it;
// these overlap in domain with the real ones so selection actually costs
// something.
var distractorTools = []struct{ name, desc string }{
	{"cancel_meeting", "Cancel a scheduled meeting by its identifier"},
	{"list_meetings", "List meetings on a given day"},
	{"send_email", "Send an email to one or more recipients"},
	{"find_contact", "Look up a contact by name"},
	{"set_reminder", "Set a personal reminder"},
	{"create_task", "Create a task in the tracker"},
	{"close_task", "Close an existing task"},
	{"search_files", "Search the workspace for files matching a pattern"},
	{"read_file", "Read the contents of a file"},
	{"write_file", "Write contents to a file"},
	{"run_tests", "Run the project test suite"},
	{"open_pull_request", "Open a pull request from a branch"},
	{"list_branches", "List branches in the repository"},
	{"get_weather", "Get the current weather for a city"},
	{"convert_currency", "Convert an amount between two currencies"},
	{"translate_text", "Translate text into another language"},
	{"summarize_document", "Summarize a document by identifier"},
	{"schedule_followup", "Schedule a follow-up after a meeting"},
	{"update_calendar", "Update an existing calendar entry"},
	{"invite_attendee", "Add an attendee to an existing meeting"},
}

// buildToolSet returns the tool list to hand the model and the computed call it
// must make. The target tool is always present; distractors pad the list.
func buildToolSet(rng *rand.Rand, distractors int, strict bool) ([]ollama.Tool, wanted) {
	distractors = min(max(distractors, 0), maxToolSet-1)

	target, want := scheduleMeetingTool(rng, strict)
	tools := []ollama.Tool{target}

	picked := pick(rng, distractorNames(), min(distractors, len(distractorTools)))
	for _, name := range picked {
		for _, d := range distractorTools {
			if d.name == name {
				tools = append(tools, ollama.Tool{
					Type: "function",
					Function: ollama.ToolFunction{
						Name: d.name, Description: d.desc,
						Parameters: objectSchema(map[string]any{
							"id": map[string]any{"type": "string", "description": "Identifier"},
						}, []string{"id"}),
					},
				})
				break
			}
		}
	}
	// Shuffle so the answer is not always first. A model that always calls the
	// first tool would otherwise pass every trial.
	rng.Shuffle(len(tools), func(i, j int) { tools[i], tools[j] = tools[j], tools[i] })
	return tools, want
}

func distractorNames() []string {
	out := make([]string, 0, len(distractorTools))
	for _, d := range distractorTools {
		out = append(out, d.name)
	}
	return out
}

// scheduleMeetingTool is the target. Its arguments are drawn from seeded pools
// so every instance is a fresh trial with a computed correct answer, and the
// prompt states each value exactly once in natural order.
func scheduleMeetingTool(rng *rand.Rand, strict bool) (ollama.Tool, wanted) {
	request := newMeetingRequest(rng)
	props := meetingProperties()
	required := []string{"title", "date", "duration_minutes", "attendees"}
	args := map[string]any{
		"title": request.title, "date": request.date.Format("2006-01-02"),
		"duration_minutes": request.duration, "attendees": request.attendees,
	}
	prompt := request.prompt()
	optional := ""
	if strict {
		prompt, required, optional = addStrictMeetingFields(rng, request, props, args, required)
	}
	return ollama.Tool{
		Type: "function",
		Function: ollama.ToolFunction{
			Name:        "schedule_meeting",
			Description: "Schedule a meeting on the calendar",
			Parameters:  objectSchema(props, required),
		},
	}, wanted{name: "schedule_meeting", prompt: prompt, args: args, optional: optional}
}

type meetingRequest struct {
	attendees []string
	title     string
	date      time.Time
	duration  int
}

func newMeetingRequest(rng *rand.Rand) meetingRequest {
	people := pick(rng, poolNames, 2)
	attendees := []string{
		strings.ToLower(people[0]) + "@example.com",
		strings.ToLower(people[1]) + "@example.com",
	}
	rawTitle := one(rng, []string{
		"Quarterly Budget Review", "Supply-Chain Sync", "Launch Readiness",
		"Hiring Pipeline Check-In", "Incident Postmortem", "Roadmap Grooming",
	})
	date := randDate(rng)
	duration := []int{15, 30, 45, 60}[rng.IntN(4)]
	return meetingRequest{attendees: attendees, title: rawTitle, date: date, duration: duration}
}

func meetingProperties() map[string]any {
	return map[string]any{
		"title": map[string]any{
			"type": "string", "description": "Meeting title, exactly as given",
		},
		"date": map[string]any{
			"type": "string", "description": "Meeting date in YYYY-MM-DD form",
		},
		"duration_minutes": map[string]any{
			"type": "integer", "description": "Length of the meeting in minutes",
		},
		"attendees": map[string]any{
			"type":        "array",
			"description": "Attendee email addresses, in the order given",
			"items":       map[string]any{"type": "string"},
		},
	}
}

func (request meetingRequest) prompt() string {
	return fmt.Sprintf(
		"Schedule a meeting titled %q on %s for %d minutes with %s and %s. "+
			"Use the tool. Do not ask for confirmation.",
		request.title, request.date.Format("January 2, 2006"), request.duration,
		request.attendees[0], request.attendees[1])
}

func addStrictMeetingFields(rng *rand.Rand, request meetingRequest, props, args map[string]any,
	required []string) (string, []string, string) {
	// A closed enum and a bounded integer: a model can be syntactically
	// perfect and still wrong, which is the point. Without these a schema
	// is satisfied by almost any object.
	room := one(rng, []string{"onsite", "remote", "hybrid"})
	props["location"] = map[string]any{
		"type": "string", "description": "One of: onsite, remote, hybrid",
		"enum": []any{"onsite", "remote", "hybrid"},
	}
	props["priority"] = map[string]any{
		"type": "integer", "description": "Priority from 1 (lowest) to 5 (highest)",
		"minimum": 1, "maximum": 5,
	}
	priority := 1 + rng.IntN(5)
	required = append(required, "location", "priority")
	args["location"] = room
	args["priority"] = priority
	prompt := fmt.Sprintf(
		"Schedule a meeting titled %q on %s for %d minutes with %s and %s. "+
			"It is %s, priority %d. Use the tool. Do not ask for confirmation.",
		request.title, request.date.Format("January 2, 2006"), request.duration,
		request.attendees[0], request.attendees[1], room, priority)

	// A declared-but-unrequested optional. Supplying it is not a failure;
	// supplying it wrong would be. This separates "invented a parameter"
	// from "used one it was offered".
	props["notes"] = map[string]any{
		"type": "string", "description": "Optional free-text notes",
	}
	return prompt, required, "notes"
}

func objectSchema(props map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}
