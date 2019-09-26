#!/bin/bash
cfg='cfg'
config=`cat $cfg | grep CTOOL_JSON | awk -F'=' '{print $2}'`
abi=`cat $cfg | grep ABI_JSON | awk -F'=' '{print $2}'`
addr=`cat $cfg | grep ADDR | awk -F'=' '{print $2}'`
ctool=`cat $cfg | grep CTOOL_BIN | awk -F'=' '{print $2}'`

name_string=$1

$ctool invoke --config $config --addr $addr --abi $abi --func getRegisterInfoByName --param $name_string 
echo "getRegisterInfoByName"
echo name_string = $name_string
