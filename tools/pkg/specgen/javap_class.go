package specgen

// JavapClass holds the parsed output of javap for one class.
type JavapClass struct {
	FullName                         string // e.g. "android.app.KeyguardManager"
	SuperClass                       string
	IsInterface                      bool
	IsAbstract                       bool
	IsFinal                          bool
	Constants                        []JavapConstant
	Methods                          []JavapMethod
	Constructors                     []JavapConstructor
	Implements                       []string
	HasUnparsedAbstractMethods       bool
	HasPackagePrivateAbstractMethods bool
	// TypeParams maps generic type-variable names declared on the class
	// header to the upper bound's fully-qualified name (with '$' for
	// nested classes, matching the spec convention). For
	// `class Foo<VH extends androidx.x.Y$Z>`, TypeParams is
	// {"VH": "androidx.x.Y$Z"}. An unbounded `<T>` defaults to
	// "java.lang.Object".
	TypeParams map[string]string
	// TypeParamOrder preserves the declaration order of TypeParams. The
	// order matters for emitting a parameterized `extends Foo<A, B>`
	// clause in the abstract-adapter Java template.
	TypeParamOrder []string
}

// JavapConstructor is a public constructor parsed from javap output.
type JavapConstructor struct {
	Params      []JavapParam
	IsPublic    bool
	IsProtected bool
}
