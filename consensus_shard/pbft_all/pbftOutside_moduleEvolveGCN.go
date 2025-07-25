package pbft_all

import (
	"blockEmulator/chain"
	"blockEmulator/consensus_shard/pbft_all/dataSupport"
	"blockEmulator/message"
	"encoding/json"
	"log"
)

// EvolveGCN Outside Module - 处理分片间消息，基于 CLPA Relay 逻辑但增加数据收集功能
type EvolveGCNRelayOutsideModule struct {
	pbftNode *PbftConsensusNode
	cdm      *dataSupport.Data_supportCLPA
}

func (erom *EvolveGCNRelayOutsideModule) HandleMessageOutsidePBFT(msgType message.MessageType, content []byte) bool {
	switch msgType {
	case message.CRelay:
		erom.handleRelay(content)
	case message.CRelayWithProof:
		erom.handleRelayWithProof(content)
	case message.CInject:
		erom.handleInjectTx(content)

	// messages about CLPA/EvolveGCN
	case message.CPartitionMsg:
		// ========== 关键增强：接收分区消息时触发数据收集 ==========
		erom.pbftNode.pl.Plog.Println("EvolveGCN: received partition message, triggering node feature collection")
		erom.pbftNode.nodeFeatureCollector.HandleRequestNodeState()
		erom.handlePartitionMsg(content)
	case message.AccountState_and_TX:
		erom.handleAccountStateAndTxMsg(content)
	case message.CPartitionReady:
		erom.handlePartitionReady(content)

	// ========== 修复：处理节点状态收集请求 ==========
	case message.CRequestNodeState:
		erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d: received node state collection request", erom.pbftNode.ShardID, erom.pbftNode.NodeID)
		// 异步处理避免阻塞其他消息
		go erom.pbftNode.nodeFeatureCollector.HandleRequestNodeState()

	default:
	}
	return true
}

// 处理中继消息（复用 CLPA 逻辑）
func (erom *EvolveGCNRelayOutsideModule) handleRelay(content []byte) {
	relay := new(message.Relay)
	err := json.Unmarshal(content, relay)
	if err != nil {
		log.Panic(err)
	}
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has received relay txs from shard %d, the senderSeq is %d\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID, relay.SenderShardID, relay.SenderSeq)
	erom.pbftNode.CurChain.Txpool.AddTxs2Pool(relay.Txs)
	erom.pbftNode.seqMapLock.Lock()
	erom.pbftNode.seqIDMap[relay.SenderShardID] = relay.SenderSeq
	erom.pbftNode.seqMapLock.Unlock()
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has handled relay txs msg\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID)
}

func (erom *EvolveGCNRelayOutsideModule) handleRelayWithProof(content []byte) {
	rwp := new(message.RelayWithProof)
	err := json.Unmarshal(content, rwp)
	if err != nil {
		log.Panic(err)
	}
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has received relay txs & proofs from shard %d, the senderSeq is %d\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID, rwp.SenderShardID, rwp.SenderSeq)
	// validate the proofs of txs
	isAllCorrect := true
	for i, tx := range rwp.Txs {
		if ok, _ := chain.TxProofVerify(tx.TxHash, &rwp.TxProofs[i]); !ok {
			isAllCorrect = false
			break
		}
	}
	if isAllCorrect {
		erom.pbftNode.CurChain.Txpool.AddTxs2Pool(rwp.Txs)
	} else {
		erom.pbftNode.pl.Plog.Println("EvolveGCN Err: wrong proof!")
	}

	erom.pbftNode.seqMapLock.Lock()
	erom.pbftNode.seqIDMap[rwp.SenderShardID] = rwp.SenderSeq
	erom.pbftNode.seqMapLock.Unlock()
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has handled relay txs msg\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID)
}

func (erom *EvolveGCNRelayOutsideModule) handleInjectTx(content []byte) {
	it := new(message.InjectTxs)
	err := json.Unmarshal(content, it)
	if err != nil {
		log.Panic(err)
	}
	erom.pbftNode.CurChain.Txpool.AddTxs2Pool(it.Txs)
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has handled injected txs msg, txs: %d \n", erom.pbftNode.ShardID, erom.pbftNode.NodeID, len(it.Txs))
}

// 处理分区消息（复用 CLPA 逻辑，但增加数据收集）
func (erom *EvolveGCNRelayOutsideModule) handlePartitionMsg(content []byte) {
	erom.pbftNode.pl.Plog.Println("EvolveGCN: received partition message from supervisor")

	// ========== 删除重复的数据收集触发，避免数据覆盖 ==========
	// 原代码：erom.pbftNode.nodeFeatureCollector.HandleRequestNodeState()

	pm := new(message.PartitionModifiedMap)
	err := json.Unmarshal(content, pm)
	if err != nil {
		log.Panic()
	}
	erom.cdm.ModifiedMap = append(erom.cdm.ModifiedMap, pm.PartitionModified)
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d : has received partition message\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID)
	erom.cdm.PartitionOn = true
}

// wait for other shards' last rounds are over
func (erom *EvolveGCNRelayOutsideModule) handlePartitionReady(content []byte) {
	pr := new(message.PartitionReady)
	err := json.Unmarshal(content, pr)
	if err != nil {
		log.Panic()
	}
	erom.cdm.P_ReadyLock.Lock()
	erom.cdm.PartitionReady[pr.FromShard] = true
	erom.cdm.P_ReadyLock.Unlock()

	erom.pbftNode.seqMapLock.Lock()
	erom.cdm.ReadySeq[pr.FromShard] = pr.NowSeqID
	erom.pbftNode.seqMapLock.Unlock()

	erom.pbftNode.pl.Plog.Printf("EvolveGCN ready message from shard %d, seqid is %d\n", pr.FromShard, pr.NowSeqID)
}

// when the message from other shard arriving, it should be added into the message pool
func (erom *EvolveGCNRelayOutsideModule) handleAccountStateAndTxMsg(content []byte) {
	at := new(message.AccountStateAndTx)
	err := json.Unmarshal(content, at)
	if err != nil {
		log.Panic()
	}
	erom.cdm.AccountStateTx[at.FromShard] = at
	erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d has added the accoutStateandTx from %d to pool\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID, at.FromShard)

	if len(erom.cdm.AccountStateTx) == int(erom.pbftNode.pbftChainConfig.ShardNums)-1 {
		erom.cdm.CollectLock.Lock()
		erom.cdm.CollectOver = true
		erom.cdm.CollectLock.Unlock()
		erom.pbftNode.pl.Plog.Printf("EvolveGCN S%dN%d has added all accoutStateandTx~~~\n", erom.pbftNode.ShardID, erom.pbftNode.NodeID)
	}
}
