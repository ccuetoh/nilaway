package consumer

import "example.com/nilaway-action/cross/source"

func Value() int { return *source.Pointer() }
