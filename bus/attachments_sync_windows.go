package bus

import "os"

// Windows does not support fsync on an opened directory. The file itself has
// already been flushed before its metadata transaction is committed.
func syncAttachmentDirectory(_ *os.File) error { return nil }
