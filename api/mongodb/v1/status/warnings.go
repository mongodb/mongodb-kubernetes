package status

type Warning string

type Warnings []Warning

const (
	SEP Warning = ";"
)

func (m Warnings) AddIfNotExists(warning Warning) Warnings {
	for _, existingWarning := range m {
		if existingWarning == warning || existingWarning == warning+SEP {
			return m
		}
	}

	// separate warnings by a ;
	for i := 0; i < len(m); i++ {
		existingWarning := m[i]
		if existingWarning[len(existingWarning)-1:] != SEP {
			m[i] += SEP
		}
	}

	return append(m, warning)
}
