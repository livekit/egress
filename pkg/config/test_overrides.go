package config

// TestOverrides is used to override the default configuration for testing purposes.
type TestOverrides struct {
	// inject failure for rooms containing this substring, useful for testing failure conditions
	FailureInjectionRoom string `yaml:"failure_injection_room"`
}
