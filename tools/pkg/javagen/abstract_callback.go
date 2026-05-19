package javagen

// AbstractCallback describes an abstract Java class whose abstract methods
// are delegated to Go via GoAbstractDispatch.
type AbstractCallback struct {
	JavaClass       string                   `yaml:"java_class"`
	GoType          string                   `yaml:"go_type"`
	Methods         []AbstractCallbackMethod `yaml:"methods"`
	TypeParamBounds []string                 `yaml:"type_param_bounds,omitempty"`
}
