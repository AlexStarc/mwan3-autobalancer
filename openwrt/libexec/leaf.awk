# Replay only the installed stock helper's selected leaf updates, and canonicalize
# the actual kernel readback. No eval, foreign targets, chains, matches or tokens.
function fail() { bad=1; exit 1 }
function number(s,    n,i,c) {
	if (s ~ /^0x[0-9a-fA-F]+$/) {
		n=0; s=tolower(substr(s,3))
		for (i=1;i<=length(s);i++) { c=index("0123456789abcdef",substr(s,i,1))-1; n=n*16+c }
		return sprintf("%.0f",n)
	}
	if (s !~ /^[0-9]+$/) fail()
	return sprintf("%.0f",s+0)
}
function mark(s,    a,n) { n=split(s,a,"/"); if(n!=2) fail(); return number(a[1]) "/" number(a[2]) }
function tokens(line,    i,c,q,n,t) {
	delete tok; q=0; n=0; t=""
	for(i=1;i<=length(line);i++) {
		c=substr(line,i,1)
		if(c=="\"") { q=!q; continue }
		if(c=="\\") fail()
		if(!q && (c==" " || c=="\t")) { if(length(t)) {tok[++n]=t;t=""}; continue }
		t=t c
	}
	if(q) fail(); if(length(t)) tok[++n]=t
	return n
}
function rule(n,    i,m,p,c,j,o,part,k,a,t) {
	m="";p="1.000";c="";j="";o="";delete seen
	for(i=3;i<=n;) {
		if(tok[i]=="-o" && !seen["out"]++) { o=tok[i+1]; if(o !~ /^[A-Za-z0-9_][A-Za-z0-9_.:-]*$/) fail(); i+=2; continue }
		if(tok[i]=="-j" && !seen["target"]++ && tok[i+1]=="MARK" && tok[i+2]=="--set-xmark") { j=mark(tok[i+3]);i+=4;continue }
		if(tok[i]!="-m") fail()
		k=tok[i+1]; if(seen[k]++) fail()
		if(k=="mark" && tok[i+2]=="--mark") {m=mark(tok[i+3]);if(substr(m,1,2)!="0/") fail();i+=4;continue}
		if(k=="statistic" && tok[i+2]=="--mode" && tok[i+3]=="random" && tok[i+4]=="--probability") {
			t=tok[i+5]; if(t !~ /^[0-9]+([.][0-9]+)?$/ || t+0<0 || t+0>1) fail()
			# Native helper uses thousandths. Allow kernel quantization (~1/2^31).
			p=sprintf("%.3f",t+0); if((p+0-t)>0.000000001 || (t-p)>0.000000001) fail()
			i+=6;continue
		}
		if(k=="comment" && tok[i+2]=="--comment") {
			c=tok[i+3]; part=split(c,a," ")
			if(part==1) {if(c!="default" && c!="unreachable" && c!="blackhole") fail()}
			else if(part==3 && a[1]=="out") {if(a[2] !~ /^[A-Za-z0-9_][A-Za-z0-9_-]*$/ || a[3]!=o) fail()}
			else if(part==3) {if(a[1] !~ /^[A-Za-z0-9_][A-Za-z0-9_-]*$/ || a[2] !~ /^[0-9]+$/ || a[3] !~ /^[0-9]+$/ || o!="") fail()}
			else fail()
			i+=4;continue
		}
		fail()
	}
	if(m=="" || j=="" || c=="") fail()
	if(split(m,a,"/")!=2) fail(); split(j,partmark,"/"); if(a[2]!=partmark[2]) fail()
	return o "|" m "|" p "|" c "|" j
}
{
	if(updates && ($0=="*mangle" || $0=="COMMIT" || $0=="")) next
	n=tokens($0); if(tok[2]!=chain) fail()
	if(tok[1]=="-N" && n==2) { exists=1;next }
	if(tok[1]=="-F" && n==2 && updates) { delete rules;count=0;exists=1;next }
	if(tok[1]!="-A" && !(updates && tok[1]=="-I")) fail()
	r=rule(n);exists=1
	if(tok[1]=="-I") {for(i=count;i>0;i--)rules[i+1]=rules[i];rules[1]=r;count++}
	else rules[++count]=r
}
END { if(bad || !exists) exit 1; for(i=1;i<=count;i++) print rules[i] }
