# ext/valkey-go

Valkey cache provider for `crema` using `valkey-go`.

## Features

- `ValkeyCacheProvider` for storing cache data in Valkey with TTL handling

## Usage

```go
import (
	cremavalkey "github.com/abema/crema/ext/valkey-go"
	"github.com/valkey-io/valkey-go"
)

client, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{"127.0.0.1:6379"}})
if err != nil {
	panic(err)
}

provider := cremavalkey.NewValkeyCacheProvider(client)
```
