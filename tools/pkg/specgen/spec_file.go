package specgen

// SpecFile is the YAML output structure.
type SpecFile struct {
	Package                     string                            `yaml:"package"`
	GoImport                    string                            `yaml:"go_import"`
	Classes                     []SpecClass                       `yaml:"classes"`
	Callbacks                   []SpecCallback                    `yaml:"callbacks,omitempty"`
	AbstractCallbacks           []SpecAbstractCallback            `yaml:"abstract_callbacks,omitempty"`
	AbstractCallbackEligibility []SpecAbstractCallbackEligibility `yaml:"abstract_callback_eligibility,omitempty"`
	Constants                   []SpecConstant                    `yaml:"constants,omitempty"`
}
