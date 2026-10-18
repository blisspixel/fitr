package top

import "github.com/blisspixel/fitr/internal/render"

func useForRole(run Run) Role {
	switch render.UseForTone(run.UseFor, run.Level == "checks") {
	case render.UseForPass:
		return RolePass
	case render.UseForWarn:
		return RoleWarning
	default:
		return RoleDefault
	}
}
