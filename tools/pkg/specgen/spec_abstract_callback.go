package specgen

// SpecAbstractCallback is an abstract class callback in the YAML spec.
// Unlike SpecCallback (which represents a Java interface), this
// represents an abstract class whose abstract methods are delegated
// to Go via GoAbstractDispatch.
type SpecAbstractCallback struct {
	JavaClass string                       `yaml:"java_class"`
	GoType    string                       `yaml:"go_type"`
	Methods   []SpecAbstractCallbackMethod `yaml:"methods"`
	// TypeParamBounds lists the upper-bound FQN of each generic type
	// parameter declared on the abstract class header, in declaration
	// order (e.g. for `class Foo<VH extends Bar>` it is `["Bar"]`). The
	// javagen template uses this to emit a bound `extends Foo<Bar>`
	// clause; the slice is empty for non-generic classes.
	TypeParamBounds []string `yaml:"type_param_bounds,omitempty"`
}
