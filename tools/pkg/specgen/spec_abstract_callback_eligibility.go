package specgen

// SpecAbstractCallbackEligibility records why specgen did or did not emit
// an abstract adapter spec for an abstract Java class.
type SpecAbstractCallbackEligibility struct {
	JavaClass string `yaml:"java_class"`
	Generated bool   `yaml:"generated"`
	Reason    string `yaml:"reason"`
}
