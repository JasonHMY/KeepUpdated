package audit

// ReadNodePackageForTest exercises the bounded reader without a host-wide scan.
func ReadNodePackageForTest(c Config, path, owner string) Report {
	s := New(c)
	s.nodePackage(path, owner)
	s.finish()
	return s.Report
}
