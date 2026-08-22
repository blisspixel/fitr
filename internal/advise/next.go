package advise

import "strconv"

const defaultRunCtx = 8192

// ResultNext is the one next command after a saved measurement.
// Inventory, advise, the TUI Result view, and HTML export all use this.
// Finish the battery before asking to persist a context.
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
	if level == "quick" || level == "checks-only" {
		return "fitr run " + name
	}
	if ctx > 0 && ctx != defaultRunCtx {
		return "fitr apply " + name
	}
	return "fitr view " + name
}

// MeasuredNext is ResultNext with the runtime's observed serving context.
// A missing observation is not 8192 and not "already applied".
func MeasuredNext(model string, repeats, measuredCtx int, level string, toolsBlocked bool, servingCtx int, servingKnown bool) string {
	next := ResultNext(model, repeats, measuredCtx, level, toolsBlocked)
	name := shellModel(model)
	if name == "" {
		name = "<model>"
	}
	apply, view := "fitr apply "+name, "fitr view "+name
	if !servingKnown || measuredCtx <= 0 {
		return next
	}
	if servingCtx == measuredCtx && next == apply {
		return view
	}
	if servingCtx != measuredCtx && next == view {
		return apply
	}
	return next
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
