package app

import (
	"encoding/json"
	"log"
	
	abci "github.com/tendermint/tendermint/abci/types"
	tmtypes "github.com/tendermint/tendermint/types"
	
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	"github.com/cosmos/cosmos-sdk/x/staking"
)

func (app *CoCoApp) ExportAppStateAndValidators(forZeroHeight bool, jailWhiteList []string,
) (appState json.RawMessage, validators []tmtypes.GenesisValidator, cp *abci.ConsensusParams, err error) {
	ctx := app.NewContext(true, abci.Header{Height: app.LastBlockHeight()})
	
	if forZeroHeight {
		app.prepForZeroHeightGenesis(ctx, jailWhiteList)
	}
	
	genState := app.mm.ExportGenesis(ctx, app.cdc)
	appState, err = codec.MarshalJSONIndent(app.cdc, genState)
	if err != nil {
		return nil, nil, nil, err
	}
	validators = staking.WriteValidators(ctx, app.stakingKeeper)
	return appState, validators, app.BaseApp.GetConsensusParams(ctx), nil
}

func (app *CoCoApp) prepForZeroHeightGenesis(ctx sdk.Context, jailWhiteList []string) {
	applyWhiteList := false
	
	if len(jailWhiteList) > 0 {
		applyWhiteList = true
	}
	
	whiteListMap := make(map[string]bool)
	
	for _, addr := range jailWhiteList {
		_, err := sdk.ValAddressFromBech32(addr)
		if err != nil {
			log.Fatal(err)
		}
		whiteListMap[addr] = true
	}
	
	app.crisisKeeper.AssertInvariants(ctx)
	
	app.stakingKeeper.IterateValidators(ctx, func(_ int64, val staking.ValidatorI) (stop bool) {
		_, err := app.distrKeeper.WithdrawValidatorCommission(ctx, val.GetOperator())
		if err != nil {
			log.Fatal(err)
		}
		return false
	})
	
	dels := app.stakingKeeper.GetAllDelegations(ctx)
	for _, delegation := range dels {
		_, err := app.distrKeeper.WithdrawDelegationRewards(ctx, delegation.DelegatorAddress, delegation.ValidatorAddress)
		if err != nil {
			log.Fatal(err)
		}
	}
	
	app.distrKeeper.DeleteAllValidatorSlashEvents(ctx)
	
	app.distrKeeper.DeleteAllValidatorHistoricalRewards(ctx)
	
	height := ctx.BlockHeight()
	ctx = ctx.WithBlockHeight(0)
	
	app.stakingKeeper.IterateValidators(ctx, func(_ int64, val staking.ValidatorI) (stop bool) {
		
		scraps := app.distrKeeper.GetValidatorOutstandingRewards(ctx, val.GetOperator()).Rewards
		feePool := app.distrKeeper.GetFeePool(ctx)
		feePool.CommunityPool = feePool.CommunityPool.Add(scraps...)
		app.distrKeeper.SetFeePool(ctx, feePool)
		
		app.distrKeeper.Hooks().AfterValidatorCreated(ctx, val.GetOperator())
		return false
	})
	
	for _, del := range dels {
		app.distrKeeper.Hooks().BeforeDelegationCreated(ctx, del.DelegatorAddress, del.ValidatorAddress)
		app.distrKeeper.Hooks().AfterDelegationModified(ctx, del.DelegatorAddress, del.ValidatorAddress)
	}
	
	ctx = ctx.WithBlockHeight(height)
	
	app.stakingKeeper.IterateRedelegations(ctx, func(_ int64, red staking.Redelegation) (stop bool) {
		for i := range red.Entries {
			red.Entries[i].CreationHeight = 0
		}
		app.stakingKeeper.SetRedelegation(ctx, red)
		return false
	})
	
	app.stakingKeeper.IterateUnbondingDelegations(ctx, func(_ int64, ubd staking.UnbondingDelegation) (stop bool) {
		for i := range ubd.Entries {
			ubd.Entries[i].CreationHeight = 0
		}
		app.stakingKeeper.SetUnbondingDelegation(ctx, ubd)
		return false
	})
	
	store := ctx.KVStore(app.keys[staking.StoreKey])
	iter := sdk.KVStoreReversePrefixIterator(store, staking.ValidatorsKey)
	counter := int16(0)
	
	for ; iter.Valid(); iter.Next() {
		addr := sdk.ValAddress(iter.Key()[1:])
		validator, found := app.stakingKeeper.GetValidator(ctx, addr)
		if !found {
			panic("expected validator, not found")
		}
		
		validator.UnbondingHeight = 0
		if applyWhiteList && !whiteListMap[addr.String()] {
			validator.Jailed = true
		}
		
		app.stakingKeeper.SetValidator(ctx, validator)
		counter++
	}
	
	iter.Close()
	
	_ = app.stakingKeeper.ApplyAndReturnValidatorSetUpdates(ctx)
	
	app.slashingKeeper.IterateValidatorSigningInfos(
		ctx,
		func(addr sdk.ConsAddress, info slashing.ValidatorSigningInfo) (stop bool) {
			info.StartHeight = 0
			app.slashingKeeper.SetValidatorSigningInfo(ctx, addr, info)
			return false
		},
	)
}
