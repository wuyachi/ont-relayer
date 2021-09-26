/*
* Copyright (C) 2020 The poly network Authors
* This file is part of The poly network library.
*
* The poly network is free software: you can redistribute it and/or modify
* it under the terms of the GNU Lesser General Public License as published by
* the Free Software Foundation, either version 3 of the License, or
* (at your option) any later version.
*
* The poly network is distributed in the hope that it will be useful,
* but WITHOUT ANY WARRANTY; without even the implied warranty of
* MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
* GNU Lesser General Public License for more details.
* You should have received a copy of the GNU Lesser General Public License
* along with The poly network . If not, see <http://www.gnu.org/licenses/>.
 */
package service

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"time"

	"github.com/polynetwork/poly-bridge/bridgesdk"

	sdk "github.com/ontio/ontology-go-sdk"
	"github.com/ontio/ontology/common"
	"github.com/ontio/ontology/smartcontract/service/native/utils"
	"github.com/polynetwork/ont-relayer/config"
	"github.com/polynetwork/ont-relayer/db"
	"github.com/polynetwork/ont-relayer/log"
	asdk "github.com/polynetwork/poly-go-sdk"
	"github.com/polynetwork/poly-go-sdk/client"
	vconfig "github.com/polynetwork/poly/consensus/vbft/config"
	common2 "github.com/polynetwork/poly/native/service/cross_chain_manager/common"
	autils "github.com/polynetwork/poly/native/service/utils"
)

var METHODS = map[string]bool{
	"add":             true,
	"remove":          true,
	"swap":            true,
	"unlock":          true,
	"addExtension":    true,
	"removeExtension": true,
	"registerAsset":   true,
	"onCrossTransfer": true,
}

type SyncService struct {
	aliaAccount    *asdk.Account
	aliaSdk        *asdk.PolySdk
	aliaSyncHeight uint32
	sideAccount    *sdk.Account
	sideSdk        *sdk.OntologySdk
	bridgeSdk      *bridgesdk.BridgeSdkPro
	sideSyncHeight uint32
	db             *db.BoltDB
	config         *config.Config
}

// NewSyncService ...
func NewSyncService(aliaAccount *asdk.Account, sideAccount *sdk.Account, aliaSdk *asdk.PolySdk, sideSdk *sdk.OntologySdk, bridgeSdk *bridgesdk.BridgeSdkPro) *SyncService {
	if !checkIfExist(config.DefConfig.DBPath) {
		os.Mkdir(config.DefConfig.DBPath, os.ModePerm)
	}
	boltDB, err := db.NewBoltDB(config.DefConfig.DBPath)
	if err != nil {
		log.Errorf("db.NewWaitingDB error:%s", err)
		os.Exit(1)
	}
	syncSvr := &SyncService{
		aliaAccount: aliaAccount,
		aliaSdk:     aliaSdk,
		sideAccount: sideAccount,
		sideSdk:     sideSdk,
		bridgeSdk:   bridgeSdk,
		db:          boltDB,
		config:      config.DefConfig,
	}
	return syncSvr
}

func (this *SyncService) Run() {
	go this.SideToAlliance()
	go this.AllianceToSide()
	go this.ProcessToAllianceCheckAndRetry()
}

func (this *SyncService) AllianceToSide() {
	currentSideChainSyncHeight, err := this.GetCurrentSideChainSyncHeight(this.aliaSdk.ChainId)
	if err != nil {
		log.Errorf("[AllianceToSide] this.GetCurrentSideChainSyncHeight error:", err)
		os.Exit(1)
	}
	this.sideSyncHeight = currentSideChainSyncHeight
	if config.DefConfig.AlliToSideForceSyncHeight > 0 {
		this.sideSyncHeight = uint32(config.DefConfig.AlliToSideForceSyncHeight)
	}

	for {
		currentAliaChainHeight, err := this.aliaSdk.GetCurrentBlockHeight()
		if err != nil {
			log.Errorf("[AllianceToSide] this.mainSdk.GetCurrentBlockHeight error:", err)
		}
		err = this.allianceToSide(this.sideSyncHeight, currentAliaChainHeight)
		if err != nil {
			log.Errorf("[AllianceToSide] this.allianceToSide error:", err)
		}
		time.Sleep(time.Duration(this.config.ScanInterval) * time.Second)
	}
}

func (this *SyncService) SideToAlliance() {
	currentAliaChainSyncHeight, err := this.GetCurrentAliaChainSyncHeight(this.GetSideChainID())
	if err != nil {
		log.Errorf("[SideToAlliance] this.GetCurrentAliaChainSyncHeight error:", err)
		os.Exit(1)
	}
	this.aliaSyncHeight = currentAliaChainSyncHeight
	if config.DefConfig.SideToAlliForceSyncHeight > 0 {
		this.aliaSyncHeight = uint32(config.DefConfig.SideToAlliForceSyncHeight)
	}

	for {
		log.Infof("[SideToAlliance] this.aliaSyncHeight %d", this.aliaSyncHeight)
		currentSideChainHeight, err := this.sideSdk.GetCurrentBlockHeight()
		if err != nil {
			log.Errorf("[SideToAlliance] this.sideSdk.GetCurrentBlockHeight error:", err)
		}
		err = this.sideToAlliance(this.aliaSyncHeight, currentSideChainHeight)
		if err != nil {
			log.Errorf("[SideToAlliance] this.sideToAlliance error:", err)
		}
		time.Sleep(time.Duration(this.config.ScanInterval) * time.Second)
	}
}

func (this *SyncService) ProcessToAllianceCheckAndRetry() {
	for {
		err := this.checkDoneTx()
		if err != nil {
			log.Errorf("[ProcessToAllianceCheckAndRetry] this.checkDoneTx error:%s", err)
		}
		err = this.retryTx()
		if err != nil {
			log.Errorf("[ProcessToAllianceCheckAndRetry] this.retryTx error:%s", err)
		}
		time.Sleep(time.Duration(this.config.ScanInterval) * time.Second)
	}
}

func (this *SyncService) isPaid(param *common2.ToMerkleValue) bool {
	c := 0
	for {
		txHash := hex.EncodeToString(param.MakeTxParam.TxHash)
		req := &bridgesdk.CheckFeeReq{Hash: txHash, ChainId: param.FromChainID}
		resp, err := this.bridgeSdk.CheckFee([]*bridgesdk.CheckFeeReq{req})
		if err != nil {
			log.Errorf("CheckFee failed:%v, TxHash:%s FromChainID:%d", err, txHash, param.FromChainID)
			time.Sleep(time.Second)
			continue
		}
		if len(resp) != 1 {
			log.Errorf("CheckFee resp invalid, length %d, TxHash:%s FromChainID:%d", len(resp), txHash, param.FromChainID)
			time.Sleep(time.Second)
			continue
		}

		switch resp[0].PayState {
		case bridgesdk.STATE_HASPAY:
			return true
		case bridgesdk.STATE_NOTPAY:
			if this.config.FeePrePaidCount > 0 {
				log.Infof("FeeNotPaidFreeCount=%d, txHash:%s is prePaid", this.config.FeePrePaidCount, txHash)
				this.config.FeePrePaidCount--
				return true
			}
			ss := "[ONT FeePrePaid Count exhausted]_[mainnet]\n" + "txHash:" + txHash
			PostDingtext(ss, "https://oapi.dingtalk.com/robot/send?access_token=63395d10b3104b3b3817db7d6d673b4cd7452b7a375e333dd07b85f17c6c9ca6")
			return false
		case bridgesdk.STATE_NOTCHECK:
			c++
			log.Errorf("CheckFee STATE_NOTCHECK, TxHash:%s FromChainID:%d, wait...", txHash, param.FromChainID)
			time.Sleep(time.Second)
			if c > 10 {
				return false
			}
			continue
		case bridgesdk.STATE_NOTPOLYPROXY:
			log.Info("tx (poly: %s, src: %s) has not POLYPROXY.", hex.EncodeToString(param.TxHash), txHash)
			return false
		}
	}
}

func (this *SyncService) allianceToSide(m, n uint32) error {
	last := time.Now()
	for i := m; i < n; i++ {
		log.Infof("[allianceToSide] start parse block %d duration %s diff %d", i, time.Now().Sub(last).String(), n-i)
		last = time.Now()
		//sync key header
		blockHeader, err := this.aliaSdk.GetHeaderByHeight(i)
		if err != nil {
			return fmt.Errorf("[allianceToSide] this.aliaSdk.GetBlockByHeight error: %s", err)
		}
		blkInfo := &vconfig.VbftBlockInfo{}
		if err := json.Unmarshal(blockHeader.ConsensusPayload, blkInfo); err != nil {
			return fmt.Errorf("[allianceToSide] unmarshal blockInfo error: %s", err)
		}
		if blkInfo.NewChainConfig != nil {
			err = this.syncHeaderToSide(i)
			if err != nil {
				return fmt.Errorf("[allianceToSide] this.syncHeaderToSide error:%s", err)
			}
		}

		//sync cross chain info
		events, err := this.aliaSdk.GetSmartContractEventByBlock(i)
		if err != nil {
			return fmt.Errorf("[allianceToSide] this.aliaSdk.GetSmartContractEventByBlock error:%s", err)
		}
		for _, event := range events {
			for _, notify := range event.Notify {
				states, ok := notify.States.([]interface{})
				if !ok {
					continue
				}
				if notify.ContractAddress != autils.CrossChainManagerContractAddress.ToHexString() {
					continue
				}
				name := states[0].(string)
				if name == "makeProof" {
					toChainID := uint64(states[2].(float64))
					if toChainID == this.GetSideChainID() {
						key := states[5].(string)
						txHash, err := this.syncProofToSide(key, i)
						if err != nil {
							log.Errorf("[allianceToSide] this.syncProofToSide error:%s", err)
							return fmt.Errorf("[allianceToSide] this.syncProofToSide error:%s", err)
						}
						if txHash != common.UINT256_EMPTY {
							log.Infof("[allianceToSide] syncProofToSide ( ont_tx: %s, poly_tx: %s )",
								txHash.ToHexString(), event.TxHash)
						}

					}
				}
			}
		}
		this.sideSyncHeight++
		if err := this.db.PutPolyHeight(i); err != nil {
			log.Errorf("failed to put poly height: %v", err)
		}
	}
	return nil
}

func (this *SyncService) sideToAlliance(m, n uint32) error {
	last := time.Now()
	for i := m; i < n; i++ {
		log.Infof("[sideToAlliance] start parse block %d duration %s diff %d", i, time.Now().Sub(last).String(), n-i)
		last = time.Now()
		//sync key header
		block, err := this.sideSdk.GetBlockByHeight(i)
		if err != nil {
			return fmt.Errorf("[sideToAlliance] this.sideSdk.GetBlockByHeight error: %s", err)
		}
		blkInfo := &vconfig.VbftBlockInfo{}
		if err := json.Unmarshal(block.Header.ConsensusPayload, blkInfo); err != nil {
			return fmt.Errorf("[sideToAlliance] unmarshal blockInfo error: %s", err)
		}
		if blkInfo.NewChainConfig != nil {
			err = this.syncHeaderToAlia(i)
			if err != nil {
				return fmt.Errorf("[sideToAlliance] this.syncHeaderToMain error:%s", err)
			}
		}

		//sync cross chain info
		events, err := this.sideSdk.GetSmartContractEventByBlock(i)
		if err != nil {
			return fmt.Errorf("[sideToAlliance] this.sideSdk.GetSmartContractEventByBlock error:%s", err)
		}
		for _, event := range events {
			if err != nil {
				return fmt.Errorf("[sideToAlliance] common.Uint256FromHexString error:%s", err)
			}
			for _, notify := range event.Notify {
				states, ok := notify.States.([]interface{})
				if !ok {
					continue
				}
				if notify.ContractAddress != utils.CrossChainContractAddress.ToHexString() {
					continue
				}
				name := states[0].(string)
				if name == "makeFromOntProof" {
					key := states[4].(string)
					txHash, err := this.syncProofToAlia(key, i)
					if err != nil {
						_, ok := err.(client.PostErr)
						if ok {
							return fmt.Errorf("[sideToAlliance] this.syncProofToAlia error:%s", err)
						} else {
							log.Errorf("[sideToAlliance] this.syncProofToAlia error:%s", err)
						}
					}
					log.Infof("[sideToAlliance] syncProofToAlia ( poly_tx: %s, ont_tx: %s )",
						txHash.ToHexString(), event.TxHash)
				}
			}
		}
		this.aliaSyncHeight++
		if err := this.db.PutOntHeight(i); err != nil {
			log.Errorf("failed to put ont height: %v", err)
		}
	}
	return nil
}

func PostDingtext(body string, DingUrl string) error {
	payload := map[string]interface{}{}
	payload["msgtype"] = "text"
	card := map[string]interface{}{}
	card["content"] = body
	payload["text"] = card
	return PostJson(DingUrl, payload)
}

func PostJson(url string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	log.Info("PostJson response Body:", string(respBody))
	return nil
}
