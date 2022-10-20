package main

import (
	"context"
	"fmt"
	"log"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
)

// func to get storage map key
func getShardID(shard *tlb.BlockInfo) string {
	return fmt.Sprintf("%d|%d", shard.Workchain, shard.Shard)
}

func getNotSeenShards(ctx context.Context, api *ton.APIClient, shard *tlb.BlockInfo, shardLastSeqno map[string]uint32) (ret []*tlb.BlockInfo, err error) {
	if no, ok := shardLastSeqno[getShardID(shard)]; ok && no == shard.SeqNo {
		return nil, nil
	}

	b, err := api.GetBlockData(ctx, shard)
	if err != nil {
		return nil, fmt.Errorf("get block data: %w", err)
	}

	parents, err := b.BlockInfo.GetParentBlocks()
	if err != nil {
		return nil, fmt.Errorf("get parent blocks (%d:%x:%d): %w", shard.Workchain, uint64(shard.Shard), shard.Shard, err)
	}

	for _, parent := range parents {
		ext, err := getNotSeenShards(ctx, api, parent, shardLastSeqno)
		if err != nil {
			return nil, err
		}
		ret = append(ret, ext...)
	}

	ret = append(ret, shard)
	return ret, nil
}

func main() {
	client := liteclient.NewConnectionPool()

	// connect to testnet archive node
	if err := client.AddConnection(context.Background(), "65.108.141.177:17439", "0MIADpLH4VQn+INHfm0FxGiuZZAA8JfTujRqQugkkA8="); err != nil {
		log.Fatalln("add connection err: ", err.Error())
		return
	}

	// initialize ton api lite connection wrapper
	api := ton.NewAPIClient(client)

	// we are looking for splits and merges in the following blocks:
	// split: (-1,8000000000000000,4230382)
	// merge: (-1,8000000000000000,4230442)
	// split: (-1,8000000000000000,4230629)
	// merge: (-1,8000000000000000,4230689)
	var masterShard, masterStartSeqNo, masterEndSeqNo uint64 = 0x8000000000000000, 4230350, 4230700
	master, err := api.LookupBlock(context.Background(), -1, int64(masterShard), uint32(masterStartSeqNo-1))
	if err != nil {
		log.Fatalln("lookup master block err: ", err.Error())
		return
	}

	// bound all requests to single lite server for consistency,
	// if it will go down, another lite server will be used
	ctx := api.Client().StickyContext(context.Background())

	// storage for last seen shard seqno
	shardLastSeqno := map[string]uint32{}

	// getting information about other work-chains and shards of first master block
	// to init storage of last seen shard seq numbers
	firstShards, err := api.GetBlockShardsInfo(ctx, master)
	if err != nil {
		log.Fatalln("get shards err:", err.Error())
		return
	}
	for _, shard := range firstShards {
		shardLastSeqno[getShardID(shard)] = shard.SeqNo
	}

	for seqNo := uint32(masterStartSeqNo); seqNo < uint32(masterEndSeqNo); seqNo++ {
		master, err = api.LookupBlock(context.Background(), -1, int64(masterShard), seqNo)
		if err != nil {
			log.Fatalln("wait next master err:", err.Error())
		}

		log.Printf("scanning %d master block...\n", master.SeqNo)

		// getting information about other work-chains and shards of master block
		currentShards, err := api.GetBlockShardsInfo(ctx, master)
		if err != nil {
			log.Fatalln("get shards err:", err.Error())
			return
		}

		// shards in master block may have holes, e.g. shard seqno 2756461, then 2756463, and no 2756462 in master chain
		// thus we need to scan a bit back in case of discovering a hole, till last seen, to fill the misses.
		var newShards []*tlb.BlockInfo
		for _, shard := range currentShards {
			notSeen, err := getNotSeenShards(ctx, api, shard, shardLastSeqno)
			if err != nil {
				log.Fatalln("get not seen shards err:", err.Error())
				return
			}
			shardLastSeqno[getShardID(shard)] = shard.SeqNo
			newShards = append(newShards, notSeen...)
		}

		var txList []*tlb.Transaction

		// for each shard block getting transactions
		for _, shard := range newShards {
			log.Printf("scanning block %d of shard %x...", shard.SeqNo, uint64(shard.Shard))

			var fetchedIDs []*tlb.TransactionID
			var after *tlb.TransactionID
			var more = true

			// load all transactions in batches with 100 transactions in each while exists
			for more {
				fetchedIDs, more, err = api.GetBlockTransactions(ctx, shard, 100, after)
				if err != nil {
					log.Fatalln("get tx ids err:", err.Error())
					return
				}

				if more {
					// set load offset for next query (pagination)
					after = fetchedIDs[len(fetchedIDs)-1]
				}

				for _, id := range fetchedIDs {
					// get full transaction by id
					tx, err := api.GetTransaction(ctx, shard, address.NewAddress(0, 0, id.AccountID), id.LT)
					if err != nil {
						log.Fatalln("get tx data err:", err.Error())
						return
					}
					txList = append(txList, tx)
				}
			}
		}

		for i, transaction := range txList {
			log.Println(i, transaction.String())
		}

		if len(txList) == 0 {
			log.Printf("no transactions in %d block\n", master.SeqNo)
		}
	}
}
