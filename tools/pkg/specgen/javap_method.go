package specgen

// JavapMethod is a public method parsed from javap output.
type JavapMethod struct {
	Name        string
	ReturnType  string // "void", "int", "boolean", "java.lang.String", etc.
	Params      []JavapParam
	IsPublic    bool
	IsProtected bool
	IsStatic    bool
	IsAbstract  bool
	IsFinal     bool
	IsBridge    bool
	IsSynthetic bool
	Throws      bool
}
