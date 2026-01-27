# ext/rueidis

Redis cache provider for `crema` using `rueidis`.

## Features

- `RedisCacheProvider` for storing cache data in Redis with TTL handling

## Usage

```go
import (
	cremarueidis "github.com/abema/crema/ext/rueidis"
	"github.com/redis/rueidis"
)

client, err := rueidis.NewClient(rueidis.ClientOption{InitAddress: []string{"127.0.0.1:6379"}})
if err != nil {
	panic(err)
}

defer client.Close()

provider := cremarueidis.NewRedisCacheProvider(client)
```
