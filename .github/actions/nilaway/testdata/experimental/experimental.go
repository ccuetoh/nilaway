package experimental

type leaf struct{ value int }
type parent struct{ child *leaf }

func StructFinding() int {
	p := &parent{}
	return p.child.value
}

func AnonymousFinding() int {
	return func() int {
		var p *int
		return *p
	}()
}
