package top

// writeReferenceNotes draws external-pack lines under a history header.
// An empty list leaves the canvas unchanged.
func writeReferenceNotes(w *lineWriter, notes []string) {
	for _, note := range notes {
		if note == "" {
			continue
		}
		if !w.line(Span{Text: note, Role: RoleMuted}) {
			return
		}
	}
}
