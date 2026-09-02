package changeset

import (
	"fmt"
	"strings"

	"github.com/epheo/dotvirt/internal/model"
)

// prBody renders the PR description the forge reviewer reads: the user's own
// message first, then the draft's semantic changes with their restart plane.
// The review happens in the forge, so dotvirt's analysis has to travel with
// the PR - a reviewer must not need the YAML diff to see what a change does.
func prBody(view model.DraftView, message, username string) string {
	var b strings.Builder
	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}
	if len(view.Items) > 0 {
		b.WriteString("## Changes\n")
		restart := false
		for _, item := range view.Items {
			name := item.Name
			if item.Namespace != "" {
				name = item.Namespace + "/" + item.Name
			}
			fmt.Fprintf(&b, "\n**%s %s**\n", item.Kind, name)
			for _, c := range item.Changes {
				line := ""
				switch c.Action {
				case "change":
					line = fmt.Sprintf("%s: %s -> %s", c.Field, c.From, c.To)
				case "add":
					line = fmt.Sprintf("%s: + %s", c.Field, c.To)
				case "remove":
					line = fmt.Sprintf("%s: - %s", c.Field, c.From)
				}
				if c.Restart {
					line += " *(restart required)*"
					restart = true
				}
				fmt.Fprintf(&b, "- %s\n", line)
			}
		}
		if restart {
			b.WriteString("\nChanges marked *restart required* apply at the VM's next power cycle; the VM keeps running until then.\n")
		}
	}
	if view.Warning != "" {
		fmt.Fprintf(&b, "\n**Warning:** %s\n", view.Warning)
	}
	fmt.Fprintf(&b, "\n---\nProposed from dotvirt by %s.\n", username)
	return b.String()
}
