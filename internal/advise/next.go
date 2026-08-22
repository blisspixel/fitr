package advise

import "strconv"

const defaultRunCtx = 8192

// ResultNext is the one next command after a saved measurement.
// Inventory, advise, the TUI Result view, and HTML export all use this.
func ResultNext(model string, repeats, ctx int, level string, toolsBlocked bool) string {
	name := shellModel(model)
	if name == "" {
		name = "<model>"
	}
	if toolsBlocked {
		return "fitr diag " + name
	}
	if repeats > 0 && repeats < 3 {
		return "fitr run " + name + " -k 3"
	}
	if ctx > 0 && ctx != defaultRunCtx {
		return "fitr apply " + name
	}
	if level == "quick" || level == "checks-only" {
		return "fitr run " + name
	}
	return "fitr view " + name
}

// AdviseNext is the one next command after a named advise verdict.
func AdviseNext(model, tier string, flagValue int) string {
	name := shellModel(model)
	if name == "" {
		name = "<model>"
	}
	if (tier == Compatible || tier == LowMemory) && flagValue > 0 {
		return "fitr run " + name + " --ctx " + strconv.Itoa(flagValue)
	}
	if tier == Compatible || tier == LowMemory {
		return "fitr run " + name
	}
	return ""
}
