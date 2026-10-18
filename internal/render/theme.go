package render

import (
	"embed"
	"fmt"
	"strings"
)

// The scorecard ships these two palettes and no others. A shared file should
// look like fitr to every reader, so there is no starter set and no user theme.
//
//go:embed themes/house-dark.toml themes/house-light.toml
var houseThemes embed.FS

// themeKeys is the whole house vocabulary. print_bg and print_text keep the
// printed page white with dark text, which is the print rule the scorecard
// already had.
var themeKeys = []string{
	"bg", "surface", "text", "muted", "accent", "success", "warning", "error", "info", "rule",
	"print_bg", "print_text",
}

// scorecardMark is the measuring frame from docs/assets/fitr-mark.svg.
// currentColor takes the accent token, so the page does not carry a second cyan.
const scorecardMark = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" role="img" aria-label="fitr"><title>fitr</title><path fill="currentColor" d="M18 8h28a10 10 0 0 1 10 10v6h-8v-6a2 2 0 0 0-2-2H18a2 2 0 0 0-2 2v28a2 2 0 0 0 2 2h38a10 10 0 0 1-10 8H18A10 10 0 0 1 8 46V18A10 10 0 0 1 18 8Z"/><rect fill="currentColor" x="28" y="28" width="28" height="16" rx="2"/></svg>`

// scorecardThemeScript switches dark, light, and system. With scripts off the
// page stays on the dark :root palette. The script is static and must not
// read result text.
const scorecardThemeScript = `<script>(function(){var key="fitr-scorecard-theme";var saved="";try{saved=localStorage.getItem(key)||""}catch(e){}if(saved==="dark"||saved==="light"||saved==="system"){document.documentElement.setAttribute("data-theme",saved)}document.addEventListener("click",function(event){var choice=event.target.getAttribute("data-theme-choice");if(choice!=="dark"&&choice!=="light"&&choice!=="system"){return}document.documentElement.setAttribute("data-theme",choice);try{localStorage.setItem(key,choice)}catch(e){}})})();</script>`

func houseTheme(name string) (map[string]string, error) {
	text, err := houseThemes.ReadFile("themes/" + name + ".toml")
	if err != nil {
		return nil, err
	}
	return parseHouseTheme(string(text))
}

func parseHouseTheme(text string) (map[string]string, error) {
	got := make(map[string]string, len(themeKeys))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("theme line %q is not an assignment", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) != 9 || !strings.HasPrefix(value, `"#`) || !strings.HasSuffix(value, `"`) {
			return nil, fmt.Errorf("theme %s must be a quoted #rrggbb color", key)
		}
		color := value[1 : len(value)-1]
		if !hexColor(color) {
			return nil, fmt.Errorf("theme %s color %s is not #rrggbb", key, color)
		}
		if _, exists := got[key]; exists {
			return nil, fmt.Errorf("theme key %s is repeated", key)
		}
		got[key] = color
	}
	if len(got) != len(themeKeys) {
		return nil, fmt.Errorf("theme has %d keys, want %d", len(got), len(themeKeys))
	}
	for _, key := range themeKeys {
		if got[key] == "" {
			return nil, fmt.Errorf("theme is missing %s", key)
		}
	}
	return got, nil
}

func hexColor(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func scorecardCSS() (string, error) {
	dark, err := houseTheme("house-dark")
	if err != nil {
		return "", err
	}
	light, err := houseTheme("house-light")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeThemeBlock(&b, ":root", dark, "dark")
	writeThemeBlock(&b, `html[data-theme="dark"]`, dark, "dark")
	writeThemeBlock(&b, `html[data-theme="light"]`, light, "light")
	writeThemeBlock(&b, `html[data-theme="system"]`, dark, "dark")
	b.WriteString(`@media (prefers-color-scheme: light){`)
	writeThemeBlock(&b, `html[data-theme="system"]`, light, "light")
	b.WriteByte('}')
	b.WriteString(scorecardStructureCSS)
	return b.String(), nil
}

func writeThemeBlock(b *strings.Builder, selector string, theme map[string]string, scheme string) {
	b.WriteString(selector)
	b.WriteByte('{')
	for _, key := range themeKeys {
		b.WriteString("--")
		b.WriteString(strings.ReplaceAll(key, "_", "-"))
		b.WriteByte(':')
		b.WriteString(theme[key])
		b.WriteByte(';')
	}
	b.WriteString("color-scheme:")
	b.WriteString(scheme)
	b.WriteByte('}')
}

// scorecardStructureCSS has no color literals. Every color comes from the
// house token block above it.
const scorecardStructureCSS = `*{box-sizing:border-box}` +
	`body{margin:0;background:var(--bg);color:var(--text);font:14px/1.45 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}` +
	`main{max-width:54rem;margin:0 auto;padding:2rem 1.25rem 4rem}` +
	`h1{font-size:1.15rem;font-weight:600;margin:0 0 .35rem}` +
	`.sub{color:var(--muted);margin:0 0 1rem}` +
	`.banner{border:1px solid var(--rule);background:var(--surface);padding:.8rem 1rem;margin:0 0 1.25rem;color:var(--muted)}` +
	`.banner strong{color:var(--text);font-weight:600}` +
	`h2{font-size:.75rem;letter-spacing:.08em;text-transform:uppercase;color:var(--muted);margin:1.6rem 0 .5rem;font-weight:600}` +
	`table{width:100%;border-collapse:collapse}` +
	`th,td{text-align:left;vertical-align:top;padding:.4rem .5rem .4rem 0;border-bottom:1px solid var(--rule)}` +
	`th{color:var(--muted);font-weight:500}` +
	`.k{color:var(--muted);width:11rem;white-space:nowrap}` +
	`.pass{color:var(--success)}.fail{color:var(--error)}.skip{color:var(--muted)}.blkd,.warn{color:var(--warning)}` +
	`.mark{color:var(--accent);display:inline-block;width:1.1rem;height:1.1rem;vertical-align:-.2rem;margin-right:.35rem}` +
	`.mark svg{width:100%;height:100%;display:block}` +
	`button{font:inherit;color:var(--muted);background:transparent;border:1px solid var(--rule);padding:.15rem .45rem;margin:0 .35rem .6rem 0}` +
	`footer{margin-top:2rem;color:var(--muted);font-size:.8rem}` +
	`@media print{body{background:var(--print-bg);color:var(--print-text)}.banner,.pass,.fail,.skip,.blkd,.warn,footer,.sub,th,.k,.mark,button{color:inherit;background:transparent}}`

func mustScorecardCSS() string {
	css, err := scorecardCSS()
	if err != nil {
		panic(err)
	}
	return css
}
