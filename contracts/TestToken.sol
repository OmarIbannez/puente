// SPDX-License-Identifier: MIT
// Minimal ERC-20 used only by the EVM adapter tests (go-ethereum's in-process
// simulated chain). It emits the same Transfer event as USDC and MXNB, which
// is all the scanner reads. Rebuild TestToken.bin with `just contracts`.
pragma solidity ^0.8.28;

contract TestToken {
    event Transfer(address indexed from, address indexed to, uint256 value);

    string public constant symbol = "TUSD";
    uint8 public constant decimals = 6;
    mapping(address => uint256) public balanceOf;

    function mint(address to, uint256 value) external {
        balanceOf[to] += value;
        emit Transfer(address(0), to, value);
    }

    function transfer(address to, uint256 value) external returns (bool) {
        balanceOf[msg.sender] -= value;
        balanceOf[to] += value;
        emit Transfer(msg.sender, to, value);
        return true;
    }
}
