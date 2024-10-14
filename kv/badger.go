package kv

import (
	"github.com/dgraph-io/badger/v4"
	"github.com/sirupsen/logrus"
)

type BadgerKVS[K ~int64, V ValueBytes[V]] struct {
	db    *badger.DB
	batch *badger.WriteBatch
	log   *logrus.Entry
}

func NewBadgerKVS[K ~int64, V ValueBytes[V]](db *badger.DB) *BadgerKVS[K, V] {
	batch := db.NewWriteBatch()
	batch.SetMaxPendingTxns(1024 * 5)

	return &BadgerKVS[K, V]{
		db:    db,
		batch: batch,
		log:   logrus.New().WithField("component", "badger-kv"),
	}
}

// Set implements KVS
func (kvs *BadgerKVS[K, V]) Set(key K, value V) {
	keyB := keyBytes(key)
	newValue := value.ToBytes()

	// err := kvs.db.View(func(txn *badger.Txn) error {
	// 	item, err := txn.Get(keyB)
	// 	if err != nil {
	// 		return err
	// 	}

	// 	return item.Value(func(val []byte) error {
	// 		if !bytes.Equal(val, newValue) {
	// 			return badger.ErrKeyNotFound
	// 		}
	// 		return nil
	// 	})
	// })
	// if err == nil {
	// 	return
	// }
	// if err != badger.ErrKeyNotFound {
	// 	kvs.log.Errorf("failed to check value: %s", err.Error())
	// 	return
	// }

	err := kvs.batch.Set(keyB, newValue)
	if err != nil {
		kvs.log.Errorf("failed to set value: %s", err.Error())
	}
	// err = kvs.db.Update(func(txn *badger.Txn) error {
	// 	return txn.Set(keyB, newValue)
	// })
	// if err != nil {
	// 	kvs.log.Errorf("failed to set value: %s", err.Error())
	// }
}

// Get implements KVS
func (kvs *BadgerKVS[K, V]) Get(key K) (value V, ok bool) {
	err := kvs.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(keyBytes(key))
		if err != nil {
			if err == badger.ErrKeyNotFound {
				ok = false
				return nil
			}
			return err
		}
		return item.Value(func(body []byte) error {
			value = value.FromBytes(body)
			ok = true
			return nil
		})
	})
	if err != nil {
		kvs.log.Errorf("failed to get value: %s", err.Error())
	}

	return value, ok
}

func (kvs *BadgerKVS[K, V]) Flush() error {
	return kvs.batch.Flush()
}

func (kvs *BadgerKVS[K, V]) Range(iterCall func(key K, value V) bool) {
	kvs.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()

			var v V
			err := item.Value(func(body []byte) error {
				v = v.FromBytes(body)
				return nil
			})
			if err != nil {
				return err
			}

			if !iterCall(bytesToKey[K](item.Key()), v) {
				return nil
			}
		}

		return nil
	})

}

func (kvs *BadgerKVS[K, V]) Close() error {
	kvs.batch.Flush()
	return kvs.db.Close()
}
