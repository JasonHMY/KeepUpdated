package audit

// CompareVersions shares the conservative numeric-release comparison with update planners.
func CompareVersions(installed, candidate string) (int, bool) {
	return compare(installed, candidate)
}
