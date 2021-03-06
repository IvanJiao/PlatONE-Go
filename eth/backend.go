// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package eth implements the Ethereum protocol.
package eth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PlatONEnetwork/PlatONE-Go/accounts"
	"github.com/PlatONEnetwork/PlatONE-Go/common"
	"github.com/PlatONEnetwork/PlatONE-Go/common/hexutil"
	"github.com/PlatONEnetwork/PlatONE-Go/consensus"
	"github.com/PlatONEnetwork/PlatONE-Go/consensus/cbft"
	istanbulBackend "github.com/PlatONEnetwork/PlatONE-Go/consensus/istanbul/backend"
	"github.com/PlatONEnetwork/PlatONE-Go/core"
	"github.com/PlatONEnetwork/PlatONE-Go/core/bloombits"
	"github.com/PlatONEnetwork/PlatONE-Go/core/cbfttypes"
	"github.com/PlatONEnetwork/PlatONE-Go/core/rawdb"
	"github.com/PlatONEnetwork/PlatONE-Go/core/types"
	"github.com/PlatONEnetwork/PlatONE-Go/core/vm"
	"github.com/PlatONEnetwork/PlatONE-Go/crypto"
	"github.com/PlatONEnetwork/PlatONE-Go/eth/downloader"
	"github.com/PlatONEnetwork/PlatONE-Go/eth/filters"
	"github.com/PlatONEnetwork/PlatONE-Go/eth/gasprice"
	"github.com/PlatONEnetwork/PlatONE-Go/ethdb"
	"github.com/PlatONEnetwork/PlatONE-Go/event"
	"github.com/PlatONEnetwork/PlatONE-Go/internal/ethapi"
	"github.com/PlatONEnetwork/PlatONE-Go/life/utils"
	"github.com/PlatONEnetwork/PlatONE-Go/log"
	"github.com/PlatONEnetwork/PlatONE-Go/miner"
	"github.com/PlatONEnetwork/PlatONE-Go/node"
	"github.com/PlatONEnetwork/PlatONE-Go/p2p"
	"github.com/PlatONEnetwork/PlatONE-Go/p2p/discover"
	"github.com/PlatONEnetwork/PlatONE-Go/params"
	"github.com/PlatONEnetwork/PlatONE-Go/rlp"
	"github.com/PlatONEnetwork/PlatONE-Go/rpc"
)

func InitInnerCallFunc(ethPtr *Ethereum) {
	var rootNode discover.Node
	var root common.NodeInfo
	if _, ok := ethPtr.engine.(consensus.Istanbul); !ok {
		rootNode = ethPtr.chainConfig.Cbft.InitialNodes[0]
		root = common.NodeInfo{
			Types:      1,
			Status:     1,
			Name:       "root",
			PublicKey:  rootNode.ID.String(),
			P2pPort:    int32(rootNode.UDP),
			ExternalIP: rootNode.IP.String(),
		}
	}

	innerCall := func(conAddr common.Address, data []byte) ([]byte, error) {
		ctx := context.Background()

		// Get the state
		state, header, err := ethPtr.APIBackend.StateAndHeaderByNumber(ctx, -1)
		if err != nil {
			return nil, err
		} else if state == nil {
			return nil, errors.New("state is nil")
		}

		from := common.Address{}
		to := &conAddr
		gas := uint64(0x999999999)
		gasPrice := (hexutil.Big)(*big.NewInt(0x333333))
		nonce := uint64(0)
		value := (hexutil.Big)(*big.NewInt(0))

		// Create new call message
		msg := types.NewMessage(from, to, nonce, value.ToInt(), gas, gasPrice.ToInt(), data, false, types.NormalTxType)

		// Get a new instance of the EVM.
		evm, vmError, err := ethPtr.APIBackend.GetEVM(ctx, msg, state, header, vm.Config{})
		if err != nil {
			return nil, err
		}

		// Setup the gas pool (also for unmetered requests)
		// and apply the message.
		gp := new(core.GasPool).AddGas(math.MaxUint64)
		res, _, _, _, err := core.ApplyMessage(evm, msg, gp)
		if err := vmError(); err != nil {
			return nil, err
		}

		return res, err
	}

	sysContractCall := func(sc *common.SystemConfig) {
		ctx := context.Background()

		// Get the state
		state, header, err := ethPtr.APIBackend.StateAndHeaderByNumber(ctx, -1)
		if state == nil || err != nil {
			return
		}

		// Create new call message
		msg := types.NewMessage(common.Address{}, nil, 1, big.NewInt(1), 0x1, big.NewInt(1), nil, false, types.NormalTxType)

		// Get a new instance of the EVM.
		evm, vmError, err := ethPtr.APIBackend.GetEVM(ctx, msg, state, header, vm.Config{})
		if err != nil {
			return
		}

		// clusure method for call Contract
		callContract := func(conAddr common.Address, data []byte) []byte {
			res, _, _ := evm.Call(vm.AccountRef(common.Address{}), conAddr, data, uint64(0xffffffffff), big.NewInt(0))
			if err := vmError(); err != nil {
				return nil
			}
			return res
		}

		// Get all system contracts' address
		var fh string = "getContractAddress"

		// Update system contract address
		for _, contractName := range common.SystemContractList {
			callParams := []interface{}{contractName, "latest"}
			btsRes := callContract(common.HexToAddress(core.CnsManagerAddr), common.GenCallData(fh, callParams))
			strRes := common.CallResAsString(btsRes)
			if !(len(strRes) == 0 || common.IsHexZeroAddress(strRes)) {
				sc.ContractAddress[contractName] = common.HexToAddress(strRes)
			}
		}

		// Get contract parameters from contract
		paramAddr := sc.ContractAddress["__sys_ParamManager"]
		if paramAddr != (common.Address{}) {
			funcName := "getTxGasLimit"
			funcParams := []interface{}{}
			res := callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				ret := common.CallResAsInt64(res)
				if ret > 0 {
					sc.SysParam.TxGasLimit = ret
				}
			}
			funcName = "getBlockGasLimit"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				ret := common.CallResAsInt64(res)
				if ret > 0 {
					sc.SysParam.BlockGasLimit = ret
				}
			}
			funcName = "getCheckContractDeployPermission"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				ret := common.CallResAsInt64(res)
				sc.SysParam.CheckContractDeployPermission = ret
			}

			funcName = "getIsProduceEmptyBlock"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				ret := common.CallResAsInt64(res)
				sc.SysParam.IsProduceEmptyBlock = ret == 1
			}
			funcName = "getIsTxUseGas"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				ret := common.CallResAsInt64(res)
				sc.SysParam.IsTxUseGas = ret == 1
			}

			funcName = "getCBFTTimeParam"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				strRes := common.CallResAsString(res)

				var cbftCfgTime common.CBFTProduceBlockCfg
				if err := json.Unmarshal([]byte(strRes), &cbftCfgTime); err != nil {
					log.Error("contract return invalid data", "result", strRes, "err", err.Error())
				} else {
					sc.SysParam.CBFTTime = cbftCfgTime
				}
			}
			funcName = "getGasContractName"
			funcParams = []interface{}{}
			res = callContract(paramAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				sc.SysParam.GasContractName = common.CallResAsString(res)
			}
		}

		if sc.SysParam.GasContractName != "" {
			cnsAddr := common.HexToAddress(core.CnsManagerAddr)
			funcName := "getContractAddress"
			funcParams := []interface{}{sc.SysParam.GasContractName, "latest"}
			res := callContract(cnsAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				sc.SysParam.GasContractAddr = common.HexToAddress(common.CallResAsString(res))
			}
		}

		// Get nodes from contract
		nodeManagerAddr := sc.ContractAddress["__sys_NodeManager"]
		if nodeManagerAddr != (common.Address{}) {
			funcName := "getAllNodes"
			funcParams := []interface{}{}
			res := callContract(nodeManagerAddr, common.GenCallData(funcName, funcParams))
			if res != nil {
				sc.SysParam.GasContractAddr = common.HexToAddress(common.CallResAsString(res))
			}

			strRes := common.CallResAsString(res)

			var tmp common.CommonResult
			if err := json.Unmarshal(utils.String2bytes(strRes), &tmp); err != nil {
				log.Warn("unmarshal consensus node list failed", "result", strRes, "err", err.Error())
			} else if tmp.RetCode != 0 {
				log.Debug("contract inner error", "code", tmp.RetCode, "msg", tmp.RetMsg)
			} else {
				sc.Nodes = tmp.Data
				if _, ok := ethPtr.engine.(consensus.Istanbul); !ok {
					hasRoot := false
					for _, node := range sc.Nodes {
						if node.PublicKey == rootNode.ID.String() {
							hasRoot = true
							break
						}
					}
					if !hasRoot {
						sc.Nodes = append(sc.Nodes, root)
					}
				}
			}
		}
	}

	common.SetSysContractCallFunc(sysContractCall)
	common.SetInnerCallFunc(innerCall)
	if _, ok := ethPtr.engine.(consensus.Istanbul); !ok {
		common.InitSystemconfig(root)
		return
	}
	common.InitSystemconfig(common.NodeInfo{})
}

type LesServer interface {
	Start(srvr *p2p.Server)
	Stop()
	Protocols() []p2p.Protocol
	SetBloomBitsIndexer(bbIndexer *core.ChainIndexer)
}

// Ethereum implements the Ethereum full node service.
type Ethereum struct {
	config      *Config
	chainConfig *params.ChainConfig

	// Channel for shutting down the service
	shutdownChan chan bool // Channel for shutting down the Ethereum

	// Handlers
	txPool          *core.TxPool
	blockchain      *core.BlockChain
	protocolManager *ProtocolManager
	lesServer       LesServer

	// DB interfaces
	chainDb ethdb.Database // Block chain database

	// ext db interfaces
	extDb ethdb.Database

	eventMux       *event.TypeMux
	engine         consensus.Engine
	accountManager *accounts.Manager

	bloomRequests chan chan *bloombits.Retrieval // Channel receiving bloom data retrieval requests
	bloomIndexer  *core.ChainIndexer             // Bloom indexer operating during block imports

	APIBackend *EthAPIBackend

	miner     *miner.Miner
	gasPrice  *big.Int
	etherbase common.Address

	networkID     uint64
	netRPCService *ethapi.PublicNetAPI

	lock sync.RWMutex // Protects the variadic fields (e.g. gas price and etherbase)
}

func (s *Ethereum) AddLesServer(ls LesServer) {
	s.lesServer = ls
	ls.SetBloomBitsIndexer(s.bloomIndexer)
}

// New creates a new Ethereum object (including the
// initialisation of the common Ethereum object)
func New(ctx *node.ServiceContext, config *Config) (*Ethereum, error) {
	// Ensure configuration values are compatible and sane
	var missingStateBlocks types.Blocks
	if config.SyncMode == downloader.LightSync {
		return nil, errors.New("can't run eth.Ethereum in light sync mode, use les.LightEthereum")
	}
	if !config.SyncMode.IsValid() {
		return nil, fmt.Errorf("invalid sync mode %d", config.SyncMode)
	}
	if config.MinerGasPrice == nil || config.MinerGasPrice.Cmp(common.Big0) <= 0 {
		log.Warn("Sanitizing invalid miner gas price", "provided", config.MinerGasPrice, "updated", DefaultConfig.MinerGasPrice)
		config.MinerGasPrice = new(big.Int).Set(DefaultConfig.MinerGasPrice)
	}

	// create extended database
	extDb, err := CreateExtDB(ctx, config, "extdb")

	// Assemble the Ethereum object
	chainDb, err := CreateDB(ctx, config, "chaindata")
	if err != nil {
		return nil, err
	}
	chainConfig, genesisHash, genesisErr := core.SetupGenesisBlock(chainDb, config.Genesis)
	if _, ok := genesisErr.(*params.ConfigCompatError); genesisErr != nil && !ok {
		return nil, genesisErr
	}
	log.Info("Initialised chain configuration", "config", chainConfig)

	blockSignatureCh := make(chan *cbfttypes.BlockSignature, 20)
	cbftResultCh := make(chan *cbfttypes.CbftResult)
	highestLogicalBlockCh := make(chan *types.Block)

	eth := &Ethereum{
		config:         config,
		chainDb:        chainDb,
		extDb:          extDb,
		chainConfig:    chainConfig,
		eventMux:       ctx.EventMux,
		accountManager: ctx.AccountManager,
		engine:         CreateConsensusEngine(ctx, chainConfig, config.MinerNotify, config.MinerNoverify, chainDb, blockSignatureCh, cbftResultCh, highestLogicalBlockCh, &config.CbftConfig),
		shutdownChan:   make(chan bool),
		networkID:      config.NetworkId,
		gasPrice:       config.MinerGasPrice,
		etherbase:      crypto.PubkeyToAddress(ctx.NodeKey().PublicKey), //config.Etherbase,
		bloomRequests:  make(chan chan *bloombits.Retrieval),
		bloomIndexer:   NewBloomIndexer(chainDb, params.BloomBitsBlocks, params.BloomConfirms),
	}

	log.Info("Initialising Ethereum protocol", "versions", ProtocolVersions, "network", config.NetworkId)

	if !config.SkipBcVersionCheck {
		bcVersion := rawdb.ReadDatabaseVersion(chainDb)
		if bcVersion != core.BlockChainVersion && bcVersion != 0 {
			return nil, fmt.Errorf("Blockchain DB version mismatch (%d / %d).\n", bcVersion, core.BlockChainVersion)
		}
		rawdb.WriteDatabaseVersion(chainDb, core.BlockChainVersion)
	}

	vmConfig := vm.Config{
		EnablePreimageRecording: config.EnablePreimageRecording,
		EWASMInterpreter:        config.EWASMInterpreter,
		EVMInterpreter:          config.EVMInterpreter,
	}
	cacheConfig := &core.CacheConfig{Disabled: config.NoPruning, TrieNodeLimit: config.TrieCache, TrieTimeLimit: config.TrieTimeout}
	common.SetCurrentInterpreterType(chainConfig.VMInterpreter)

	funcSyncCBFTParam := cbft.ReloadCBFTParams
	eth.blockchain, missingStateBlocks, err = core.NewBlockChain(chainDb, extDb, cacheConfig, eth.chainConfig, eth.engine, vmConfig, eth.shouldPreserve, funcSyncCBFTParam)
	if err != nil {
		return nil, err
	}
	blockChainCache := core.NewBlockChainCache(eth.blockchain)

	// Rewind the chain in case of an incompatible config upgrade.
	if compat, ok := genesisErr.(*params.ConfigCompatError); ok {
		log.Warn("Rewinding chain to upgrade configuration", "err", compat)
		eth.blockchain.SetHead(compat.RewindTo)
		rawdb.WriteChainConfig(chainDb, genesisHash, chainConfig)
	}
	eth.bloomIndexer.Start(eth.blockchain)

	eth.APIBackend = &EthAPIBackend{eth, nil}
	gpoParams := config.GPO
	if gpoParams.Default == nil {
		gpoParams.Default = config.MinerGasPrice
	}
	eth.APIBackend.gpo = gasprice.NewOracle(eth.APIBackend, gpoParams)

	// set init system param function, then reload cbft param before start up miner

	InitInnerCallFunc(eth)
	if common.SysCfg != nil {
		common.SysCfg.UpdateSystemConfig()
	}

	if len(missingStateBlocks) != 0 {
		log.Info("start to replay blocks!", "Number", len(missingStateBlocks))
		_, err := eth.blockchain.InsertChain(missingStateBlocks)
		if err != nil {
			return nil, err
		}
	}

	if _, ok := eth.engine.(consensus.Bft); ok {
		log.Trace("Load system config after start up eth")
		cbft.ReloadCBFTParams()
	}

	if config.TxPool.Journal != "" {
		config.TxPool.Journal = ctx.ResolvePath(config.TxPool.Journal)
	}
	//eth.txPool = core.NewTxPool(config.TxPool, eth.chainConfig, eth.blockchain)
	eth.txPool = core.NewTxPool(config.TxPool, eth.chainConfig, blockChainCache, chainDb, eth.extDb)
	log.Debug("Transaction pool info", "pool", eth.txPool)


	// modify by platone remove consensusCache
	//var consensusCache *cbft.Cache = cbft.NewCache(eth.blockchain)
	recommit := config.MinerRecommit
	if common.SysCfg != nil {
		recommit = time.Duration(common.SysCfg.GetCBFTTime().BlockInterval) * time.Second
	}
	eth.miner = miner.New(eth, eth.chainConfig, eth.EventMux(), eth.engine, recommit, config.MinerGasFloor, config.MinerGasCeil, eth.isLocalBlock, blockSignatureCh, cbftResultCh, highestLogicalBlockCh, blockChainCache)
	eth.miner.SetEtherbase(crypto.PubkeyToAddress(ctx.NodeKey().PublicKey))
	eth.miner.SetExtra(makeExtraData(config.MinerExtraData))

	if _, ok := eth.engine.(consensus.Bft); ok {
		cbft.SetBlockChainCache(blockChainCache)
		cbft.SetBackend(eth.blockchain, eth.txPool)
	}

	if eth.protocolManager, err = NewProtocolManager(eth.chainConfig, config.SyncMode, config.NetworkId, eth.eventMux, eth.txPool, eth.engine, eth.blockchain, chainDb); err != nil {
		return nil, err
	}

	var rootNodes []discover.Node
	if _, ok := eth.engine.(consensus.Istanbul); !ok {
		rootNodes = chainConfig.Cbft.InitialNodes
		if len(rootNodes) != 0 {
			p2p.AddRootPeer(rootNodes[0])
			p2p.SetRootNode(&rootNodes[0])
		}
	}
	return eth, nil
}

func makeExtraData(extra []byte) []byte {
	if len(extra) == 0 {
		// create default extradata
		extra, _ = rlp.EncodeToBytes([]interface{}{
			uint(params.VersionMajor<<16 | params.VersionMinor<<8 | params.VersionPatch),
			"platone",
			runtime.Version(),
			runtime.GOOS,
		})
	}
	if uint64(len(extra)) > params.MaximumExtraDataSize {
		log.Warn("Miner extra data exceed limit", "extra", hexutil.Bytes(extra), "limit", params.MaximumExtraDataSize)
		extra = nil
	}
	return extra
}

// CreateDB creates the chain database.
func CreateDB(ctx *node.ServiceContext, config *Config, name string) (ethdb.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	if db, ok := db.(*ethdb.LDBDatabase); ok {
		db.Meter("eth/db/chaindata/")
	}
	return db, nil
}

// create extended database
func CreateExtDB(ctx *node.ServiceContext, config *Config, name string) (ethdb.Database, error) {
	db, err := ctx.OpenDatabase(name, config.DatabaseCache, config.DatabaseHandles)
	if err != nil {
		return nil, err
	}
	if db, ok := db.(*ethdb.LDBDatabase); ok {
		db.Meter("eth/db/extdb/")
	}
	return db, nil
}

// CreateConsensusEngine creates the required type of consensus engine instance for an Ethereum service
func CreateConsensusEngine(ctx *node.ServiceContext, chainConfig *params.ChainConfig, notify []string, noverify bool, db ethdb.Database,
	blockSignatureCh chan *cbfttypes.BlockSignature, cbftResultCh chan *cbfttypes.CbftResult, highestLogicalBlockCh chan *types.Block, cbftConfig *CbftConfig) consensus.Engine {
	// If proof-of-authority is requested, set it up
	if chainConfig.Cbft != nil {
		if cbftConfig.Period < 1 {
			chainConfig.Cbft.Period = 1
		} else {
			chainConfig.Cbft.Period = cbftConfig.Period
		}
		chainConfig.Cbft.Epoch = cbftConfig.Epoch
		chainConfig.Cbft.MaxLatency = cbftConfig.MaxLatency
		chainConfig.Cbft.LegalCoefficient = cbftConfig.LegalCoefficient
		chainConfig.Cbft.Duration = cbftConfig.Duration
		return cbft.New(chainConfig.Cbft, blockSignatureCh, cbftResultCh, highestLogicalBlockCh)
	} else if chainConfig.Istanbul != nil {
		//if chainConfig.Istanbul.Epoch != 0 {
		//	config.Istanbul.Epoch = chainConfig.Istanbul.Epoch
		//}
		//config.Istanbul.ProposerPolicy = istanbul.ProposerPolicy(chainConfig.Istanbul.ProposerPolicy)
		return istanbulBackend.New(chainConfig.Istanbul, ctx.NodeKey(), db)
	}
	return nil
}

// APIs return the collection of RPC services the ethereum package offers.
// NOTE, some of these services probably need to be moved to somewhere else.
func (s *Ethereum) APIs() []rpc.API {
	apis := ethapi.GetAPIs(s.APIBackend)

	// Append any APIs exposed explicitly by the consensus engine
	apis = append(apis, s.engine.APIs(s.BlockChain())...)

	// Append all the local APIs and return
	return append(apis, []rpc.API{
		{
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicEthereumAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   NewPublicMinerAPI(s),
			Public:    true,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   downloader.NewPublicDownloaderAPI(s.protocolManager.downloader, s.eventMux),
			Public:    true,
		}, {
			Namespace: "miner",
			Version:   "1.0",
			Service:   NewPrivateMinerAPI(s),
			Public:    false,
		}, {
			Namespace: "eth",
			Version:   "1.0",
			Service:   filters.NewPublicFilterAPI(s.APIBackend, false),
			Public:    true,
		}, {
			Namespace: "admin",
			Version:   "1.0",
			Service:   NewPrivateAdminAPI(s),
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPublicDebugAPI(s),
			Public:    true,
		}, {
			Namespace: "debug",
			Version:   "1.0",
			Service:   NewPrivateDebugAPI(s.chainConfig, s),
		}, {
			Namespace: "net",
			Version:   "1.0",
			Service:   s.netRPCService,
			Public:    true,
		},
	}...)
}

func (s *Ethereum) ResetWithGenesisBlock(gb *types.Block) {
	s.blockchain.ResetWithGenesisBlock(gb)
}

func (s *Ethereum) Etherbase() (eb common.Address, err error) {
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()

	if etherbase != (common.Address{}) {
		return etherbase, nil
	}
	if wallets := s.AccountManager().Wallets(); len(wallets) > 0 {
		if accounts := wallets[0].Accounts(); len(accounts) > 0 {
			etherbase := accounts[0].Address

			s.lock.Lock()
			s.etherbase = etherbase
			s.lock.Unlock()

			log.Info("Etherbase automatically configured", "address", etherbase)
			return etherbase, nil
		}
	}
	return common.Address{}, fmt.Errorf("etherbase must be explicitly specified")
}

// isLocalBlock checks whether the specified block is mined
// by local miner accounts.
//
// We regard two types of accounts as local miner account: etherbase
// and accounts specified via `txpool.locals` flag.
func (s *Ethereum) isLocalBlock(block *types.Block) bool {
	author, err := s.engine.Author(block.Header())
	if err != nil {
		log.Warn("Failed to retrieve block author", "number", block.NumberU64(), "hash", block.Hash(), "err", err)
		return false
	}
	// Check whether the given address is etherbase.
	s.lock.RLock()
	etherbase := s.etherbase
	s.lock.RUnlock()
	if author == etherbase {
		return true
	}
	// Check whether the given address is specified by `txpool.local`
	// CLI flag.
	for _, account := range s.config.TxPool.Locals {
		if account == author {
			return true
		}
	}
	return false
}

// shouldPreserve checks whether we should preserve the given block
// during the chain reorg depending on whether the author of block
// is a local account.
func (s *Ethereum) shouldPreserve(block *types.Block) bool {
	// The reason we need to disable the self-reorg preserving for clique
	// is it can be probable to introduce a deadlock.
	//
	// e.g. If there are 7 available signers
	//
	// r1   A
	// r2     B
	// r3       C
	// r4         D
	// r5   A      [X] F G
	// r6    [X]
	//
	// In the round5, the inturn signer E is offline, so the worst case
	// is A, F and G sign the block of round5 and reject the block of opponents
	// and in the round6, the last available signer B is offline, the whole
	// network is stuck.
	return s.isLocalBlock(block)
}

// SetEtherbase sets the mining reward address.
func (s *Ethereum) SetEtherbase(etherbase common.Address) {
	s.lock.Lock()
	s.etherbase = etherbase
	s.lock.Unlock()

	s.miner.SetEtherbase(etherbase)
}

// StartMining starts the miner with the given number of CPU threads. If mining
// is already running, this method adjust the number of threads allowed to use
// and updates the minimum price required by the transaction pool.
func (s *Ethereum) StartMining(threads int) error {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		log.Info("Updated mining threads", "threads", threads)
		if threads == 0 {
			threads = -1 // Disable the miner from within
		}
		th.SetThreads(threads)
	}
	// If the miner was not running, initialize it
	if !s.IsMining() {
		log.Info("the miner was not running, initialize it")
		// Propagate the initial price point to the transaction pool
		s.lock.RLock()
		price := s.gasPrice
		s.lock.RUnlock()
		s.txPool.SetGasPrice(price)

		// Configure the local mining address
		eb, err := s.Etherbase()
		if err != nil {
			log.Error("Cannot start mining without etherbase", "err", err)
			if _, ok := s.engine.(consensus.Bft); ok {
				panic("Cannot start mining without etherbase")
			}
			return fmt.Errorf("etherbase missing: %v", err)
		}
		// If mining is started, we can disable the transaction rejection mechanism
		// introduced to speed sync times.
		atomic.StoreUint32(&s.protocolManager.acceptTxs, 1)
		go s.miner.Start(eb)
	}
	return nil
}

// StopMining terminates the miner, both at the consensus engine level as well as
// at the block creation level.
func (s *Ethereum) StopMining() {
	// Update the thread count within the consensus engine
	type threaded interface {
		SetThreads(threads int)
	}
	if th, ok := s.engine.(threaded); ok {
		th.SetThreads(-1)
	}
	// Stop the block creating itself
	s.miner.Stop()
}

func (s *Ethereum) IsMining() bool                     { return s.miner.Mining() }
func (s *Ethereum) Miner() *miner.Miner                { return s.miner }
func (s *Ethereum) ExtendedDb() ethdb.Database         { return s.extDb }
func (s *Ethereum) AccountManager() *accounts.Manager  { return s.accountManager }
func (s *Ethereum) BlockChain() *core.BlockChain       { return s.blockchain }
func (s *Ethereum) TxPool() *core.TxPool               { return s.txPool }
func (s *Ethereum) EventMux() *event.TypeMux           { return s.eventMux }
func (s *Ethereum) Engine() consensus.Engine           { return s.engine }
func (s *Ethereum) ChainDb() ethdb.Database            { return s.chainDb }
func (s *Ethereum) IsListening() bool                  { return true } // Always listening
func (s *Ethereum) EthVersion() int                    { return int(s.protocolManager.SubProtocols[0].Version) }
func (s *Ethereum) NetVersion() uint64                 { return s.networkID }
func (s *Ethereum) Downloader() *downloader.Downloader { return s.protocolManager.downloader }

// Protocols implements node.Service, returning all the currently configured
// network protocols to start.
func (s *Ethereum) Protocols() []p2p.Protocol {
	if s.lesServer == nil {
		return s.protocolManager.SubProtocols
	}
	return append(s.protocolManager.SubProtocols, s.lesServer.Protocols()...)
}

// Start implements node.Service, starting all internal goroutines needed by the
// Ethereum protocol implementation.
func (s *Ethereum) Start(srvr *p2p.Server) error {

	// Start the bloom bits servicing goroutines
	s.startBloomHandlers(params.BloomBitsBlocks)

	// Start the RPC service
	s.netRPCService = ethapi.NewPublicNetAPI(srvr, s.NetVersion())

	// Figure out a max peers count based on the server limits
	maxPeers := srvr.MaxPeers
	if s.config.LightServ > 0 {
		if s.config.LightPeers >= srvr.MaxPeers {
			return fmt.Errorf("invalid peer config: light peer count (%d) >= total peer count (%d)", s.config.LightPeers, srvr.MaxPeers)
		}
		maxPeers -= s.config.LightPeers
	}
	// Start the networking layer and the light server if requested
	s.protocolManager.Start(maxPeers)

	if _, ok := s.engine.(consensus.Istanbul); ok {
		for _, n := range p2p.GetBootNodes() {
			srvr.AddPeer(discover.NewNode(n.ID, n.IP, n.UDP, n.TCP))
		}
	} else if engine, ok := s.engine.(consensus.Bft); ok {
		engine.SetPrivateKey(srvr.Config.PrivateKey)
		if flag, err := engine.IsConsensusNode(); flag && err == nil {
			for _, n := range s.chainConfig.Cbft.InitialNodes {
				srvr.AddConsensusPeer(discover.NewNode(n.ID, n.IP, n.UDP, n.TCP))
			}
		}
	}
	s.StartMining(1)
	if s.lesServer != nil {
		s.lesServer.Start(srvr)
	}
	return nil
}

// Stop implements node.Service, terminating all internal goroutines used by the
// Ethereum protocol.
func (s *Ethereum) Stop() error {
	s.bloomIndexer.Close()
	s.blockchain.Stop()
	s.engine.Close()
	s.protocolManager.Stop()
	if s.lesServer != nil {
		s.lesServer.Stop()
	}
	s.txPool.Stop()
	s.miner.Stop()
	s.eventMux.Stop()

	s.chainDb.Close()
	s.extDb.Close()
	close(s.shutdownChan)
	return nil
}
