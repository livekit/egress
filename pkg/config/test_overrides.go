package config

// TestOverrides is used to override the default configuration for testing purposes.
type TestOverrides struct {
	// inject failure for rooms containing this substring, useful for testing failure conditions
	FailureInjectionRoom string `yaml:"failure_injection_room"`
	// simulate a retryable room disconnect for rooms containing this substring, once
	// the egress is active, to test that the partial output is uploaded on failure
	DisconnectInjectionRoom string `yaml:"disconnect_injection_room"`
}
