package network

const (
	// EphemeralRangeStart [TEMP]
	EphemeralRangeStart = 32768
	// EphemeralRangeEnd [TEMP]
	EphemeralRangeEnd = 60999
)

// IsEphemeralPort [PLACEHOLDER]
func IsEphemeralPort(port int) bool {
	return port >= EphemeralRangeStart && port <= EphemeralRangeEnd
}
