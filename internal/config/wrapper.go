package config

import "strings"

// WrapperContext provides values for template expansion in exec-wrapper args.
type WrapperContext struct {
	Rig           string
	Polecat       string
	InstallPrefix string
	WorkDir       string
	WorkspaceName string // pre-computed: <installPrefix>-<rig>--<polecat>
}

// ExpandWrapper replaces {{var}} placeholders in wrapper args.
func ExpandWrapper(wrapper []string, ctx WrapperContext) []string {
	if len(wrapper) == 0 {
		return nil
	}
	replacer := strings.NewReplacer(
		"{{workspace}}", ctx.WorkspaceName,
		"{{rig}}", ctx.Rig,
		"{{polecat}}", ctx.Polecat,
		"{{install_prefix}}", ctx.InstallPrefix,
		"{{work_dir}}", ctx.WorkDir,
	)
	expanded := make([]string, len(wrapper))
	for i, arg := range wrapper {
		expanded[i] = replacer.Replace(arg)
	}
	return expanded
}
