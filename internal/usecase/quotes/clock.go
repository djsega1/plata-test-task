package quotes

import "time"

type Clock interface {
	Now() time.Time
}
