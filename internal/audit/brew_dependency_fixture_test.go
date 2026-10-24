package audit

// BrewDependencyFixtureReport reads only the isolated Homebrew fixture.
func BrewDependencyFixtureReport(config Config) Report {
	s := New(config)
	s.brew()
	s.finish()
	return s.Report
}
