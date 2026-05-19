package javagen

// Spec represents a per-package Java API specification loaded from YAML.
type Spec struct {
	Package                     string                        `yaml:"package"`
	GoImport                    string                        `yaml:"go_import"`
	Classes                     []Class                       `yaml:"classes"`
	Callbacks                   []Callback                    `yaml:"callbacks"`
	AbstractCallbacks           []AbstractCallback            `yaml:"abstract_callbacks"`
	AbstractCallbackEligibility []AbstractCallbackEligibility `yaml:"abstract_callback_eligibility"`
	Constants                   []Constant                    `yaml:"constants"`
	IntentExtras                []IntentExtra                 `yaml:"intent_extras"`
}
