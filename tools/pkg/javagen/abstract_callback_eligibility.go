package javagen

// AbstractCallbackEligibility records specgen's structural decision for an
// abstract Java class considered for adapter generation.
type AbstractCallbackEligibility struct {
	JavaClass string `yaml:"java_class"`
	Generated bool   `yaml:"generated"`
	Reason    string `yaml:"reason"`
}
