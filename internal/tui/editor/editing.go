package editor

// ---------------------------------------------------------------------------
// Editing operations
// ---------------------------------------------------------------------------

// InsertText inserts a multi-line string at the current cursor position.
// Newlines are handled via insertNewline. No-op if ReadOnly.
func (m *Model) InsertText(text string) {
	if m.ReadOnly {
		return
	}
	for _, r := range text {
		switch r {
		case '\n':
			m.insertNewline()
		case '\r':
			// Skip carriage returns (normalize \r\n to \n)
		default:
			m.insertRune(r)
		}
	}
}

func (m *Model) insertRune(r rune) {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	newLine := make([]rune, 0, len(line)+1)
	newLine = append(newLine, line[:m.col]...)
	newLine = append(newLine, r)
	newLine = append(newLine, line[m.col:]...)
	m.lines[m.row] = newLine
	m.col++
}

func (m *Model) insertNewline() {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	after := make([]rune, len(line[m.col:]))
	copy(after, line[m.col:])
	m.lines[m.row] = line[:m.col]
	// Insert new line after current
	newLines := make([][]rune, 0, len(m.lines)+1)
	newLines = append(newLines, m.lines[:m.row+1]...)
	newLines = append(newLines, after)
	newLines = append(newLines, m.lines[m.row+1:]...)
	m.lines = newLines
	m.row++
	m.col = 0
}

func (m *Model) deleteBack() {
	if m.ReadOnly {
		return
	}
	if m.col > 0 {
		line := m.currentLine()
		m.lines[m.row] = append(line[:m.col-1], line[m.col:]...)
		m.col--
	} else if m.row > 0 {
		// Merge with previous line
		prev := m.lines[m.row-1]
		m.col = len(prev)
		m.lines[m.row-1] = append(prev, m.currentLine()...)
		m.lines = append(m.lines[:m.row], m.lines[m.row+1:]...)
		m.row--
	}
}

func (m *Model) deleteForward() {
	if m.ReadOnly {
		return
	}
	line := m.currentLine()
	if m.col < len(line) {
		m.lines[m.row] = append(line[:m.col], line[m.col+1:]...)
	} else if m.row < len(m.lines)-1 {
		// Merge with next line
		m.lines[m.row] = append(line, m.lines[m.row+1]...)
		m.lines = append(m.lines[:m.row+1], m.lines[m.row+2:]...)
	}
}

func (m *Model) tabIndent() {
	if m.ReadOnly {
		return
	}
	// Indent to match the leading whitespace of the line above.
	indent := "\t"
	if m.row > 0 {
		above := m.lines[m.row-1]
		indent = ""
		for _, r := range above {
			if r == ' ' || r == '\t' {
				indent += string(r)
			} else {
				break
			}
		}
		if indent == "" {
			indent = "\t"
		}
	}
	for _, r := range indent {
		m.insertRune(r)
	}
}
