package audit

// MCPFixtureReport exercises the real readers without a host-wide scan or commands.
func MCPFixtureReport(config Config, packagePath string, check bool) Report {
	s := New(config)
	s.brew()
	s.nodePackage(packagePath, "npm")
	s.configs()
	if check {
		s.dependencies()
		s.check()
	}
	s.finish()
	return s.Report
}
