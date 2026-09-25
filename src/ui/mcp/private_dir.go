package mcp

import (
 "errors"
 "os"
 "path/filepath"
 "strings"
)

// ValidatePrivateDataDir rejects public-static descendants, including symlinks.
func ValidatePrivateDataDir(dataDir,publicDir string)error{
 if dataDir==""{return errors.New("private MCP data directory required")}
 if err:=os.MkdirAll(dataDir,0700);err!=nil{return err}
 data,err:=filepath.EvalSymlinks(dataDir);if err!=nil{return err};data,err=filepath.Abs(data);if err!=nil{return err}
 public,err:=filepath.Abs(publicDir);if err!=nil{return err}
 if resolved,e:=filepath.EvalSymlinks(public);e==nil{public=resolved}
 rel,err:=filepath.Rel(public,data);if err!=nil{return err}
 if rel=="." || (rel!=".." && !strings.HasPrefix(rel,".."+string(filepath.Separator))){return errors.New("MCP_DATA_DIR must not be inside public statics")}
 return nil
}
