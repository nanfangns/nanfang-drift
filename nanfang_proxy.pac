function FindProxyForURL(url, host) {
    host = host.toLowerCase();
    if (isPlainHostName(host) ||
        shExpMatch(host, "localhost") ||
        isInNet(host, "127.0.0.0", "255.0.0.0") ||
        isInNet(host, "10.0.0.0", "255.0.0.0") ||
        isInNet(host, "172.16.0.0", "255.240.0.0") ||
        isInNet(host, "192.168.0.0", "255.255.0.0") ||
        isInNet(host, "100.64.0.0", "255.192.0.0") ||
        isInNet(host, "169.254.0.0", "255.255.0.0")) {
        return "DIRECT";
    }
    if (host === 'ldcstore.com' || dnsDomainIs(host, '.ldcstore.com') ||
        host === 'ldstore.cc.cd' || dnsDomainIs(host, '.ldstore.cc.cd') ||
        host === 'qzz.io' || dnsDomainIs(host, '.qzz.io') ||
        host === 'linux.do' || dnsDomainIs(host, '.linux.do') ||
        host === 'workers.dev' || dnsDomainIs(host, '.workers.dev') ||
        host === 'tacool.com' || dnsDomainIs(host, '.tacool.com') ||
        host === 'cloudflareinsights.com' || dnsDomainIs(host, '.cloudflareinsights.com') ||
        host === 'cloudflare.com' || dnsDomainIs(host, '.cloudflare.com') ||
        host === 'google.com' || dnsDomainIs(host, '.google.com') ||
        host === 'google.cn' || dnsDomainIs(host, '.google.cn') ||
        host === 'googleapis.com' || dnsDomainIs(host, '.googleapis.com') ||
        host === 'googleusercontent.com' || dnsDomainIs(host, '.googleusercontent.com') ||
        host === 'gstatic.com' || dnsDomainIs(host, '.gstatic.com') ||
        host === 'gvt1.com' || dnsDomainIs(host, '.gvt1.com') ||
        host === 'gvt2.com' || dnsDomainIs(host, '.gvt2.com') ||
        host === 'ggpht.com' || dnsDomainIs(host, '.ggpht.com') ||
        host === 'googlevideo.com' || dnsDomainIs(host, '.googlevideo.com') ||
        host === 'android.com' || dnsDomainIs(host, '.android.com') ||
        host === 'youtube.com' || dnsDomainIs(host, '.youtube.com') ||
        host === 'ytimg.com' || dnsDomainIs(host, '.ytimg.com')) {
        return "PROXY 127.0.0.1:7890; SOCKS5 127.0.0.1:7890";
    }
    if (dnsDomainIs(host, '.cn') ||
        dnsDomainIs(host, '.中国') ||
        dnsDomainIs(host, '.公司') ||
        dnsDomainIs(host, '.网络') ||
        host === 'douyin.com' || dnsDomainIs(host, '.douyin.com') ||
        host === 'douyinpic.com' || dnsDomainIs(host, '.douyinpic.com') ||
        host === 'douyinstatic.com' || dnsDomainIs(host, '.douyinstatic.com') ||
        host === 'douyinvod.com' || dnsDomainIs(host, '.douyinvod.com') ||
        host === 'douyinliving.com' || dnsDomainIs(host, '.douyinliving.com') ||
        host === 'amemv.com' || dnsDomainIs(host, '.amemv.com') ||
        host === 'amemv.net' || dnsDomainIs(host, '.amemv.net') ||
        host === 'ixigua.com' || dnsDomainIs(host, '.ixigua.com') ||
        host === 'snssdk.com' || dnsDomainIs(host, '.snssdk.com') ||
        host === 'pstatp.com' || dnsDomainIs(host, '.pstatp.com') ||
        host === 'toutiao.com' || dnsDomainIs(host, '.toutiao.com') ||
        host === 'byteimg.com' || dnsDomainIs(host, '.byteimg.com') ||
        host === 'bytedance.com' || dnsDomainIs(host, '.bytedance.com') ||
        host === 'bytedance.net' || dnsDomainIs(host, '.bytedance.net') ||
        host === 'bytecdn.cn' || dnsDomainIs(host, '.bytecdn.cn') ||
        host === 'bytecdn.com' || dnsDomainIs(host, '.bytecdn.com') ||
        host === 'zijieapi.com' || dnsDomainIs(host, '.zijieapi.com') ||
        host === 'volces.com' || dnsDomainIs(host, '.volces.com') ||
        host === 'volccdn.com' || dnsDomainIs(host, '.volccdn.com') ||
        host === 'volcengine.com' || dnsDomainIs(host, '.volcengine.com') ||
        host === 'feelgood.cn' || dnsDomainIs(host, '.feelgood.cn') ||
        host === 'ibytedtos.com' || dnsDomainIs(host, '.ibytedtos.com') ||
        host === 'ibytedapm.com' || dnsDomainIs(host, '.ibytedapm.com') ||
        host === 'bdurl.net' || dnsDomainIs(host, '.bdurl.net') ||
        host === 'bdstatic.com' || dnsDomainIs(host, '.bdstatic.com') ||
        host === 'baidu.com' || dnsDomainIs(host, '.baidu.com') ||
        host === 'baidubce.com' || dnsDomainIs(host, '.baidubce.com') ||
        host === 'bcebos.com' || dnsDomainIs(host, '.bcebos.com') ||
        host === 'qq.com' || dnsDomainIs(host, '.qq.com') ||
        host === 'gtimg.com' || dnsDomainIs(host, '.gtimg.com') ||
        host === 'qpic.cn' || dnsDomainIs(host, '.qpic.cn') ||
        host === 'weixin.qq.com' || dnsDomainIs(host, '.weixin.qq.com') ||
        host === 'wechat.com' || dnsDomainIs(host, '.wechat.com') ||
        host === 'tenpay.com' || dnsDomainIs(host, '.tenpay.com') ||
        host === 'alicdn.com' || dnsDomainIs(host, '.alicdn.com') ||
        host === 'aliyun.com' || dnsDomainIs(host, '.aliyun.com') ||
        host === 'alipay.com' || dnsDomainIs(host, '.alipay.com') ||
        host === 'taobao.com' || dnsDomainIs(host, '.taobao.com') ||
        host === 'tmall.com' || dnsDomainIs(host, '.tmall.com') ||
        host === 'mmstat.com' || dnsDomainIs(host, '.mmstat.com') ||
        host === 'uc.cn' || dnsDomainIs(host, '.uc.cn') ||
        host === 'jd.com' || dnsDomainIs(host, '.jd.com') ||
        host === 'jd.hk' || dnsDomainIs(host, '.jd.hk') ||
        host === 'jdpay.com' || dnsDomainIs(host, '.jdpay.com') ||
        host === '360buyimg.com' || dnsDomainIs(host, '.360buyimg.com') ||
        host === 'bilibili.com' || dnsDomainIs(host, '.bilibili.com') ||
        host === 'biliapi.com' || dnsDomainIs(host, '.biliapi.com') ||
        host === 'bilivideo.com' || dnsDomainIs(host, '.bilivideo.com') ||
        host === 'hdslb.com' || dnsDomainIs(host, '.hdslb.com') ||
        host === 'kuaishou.com' || dnsDomainIs(host, '.kuaishou.com') ||
        host === 'ksapisrv.com' || dnsDomainIs(host, '.ksapisrv.com') ||
        host === 'gifshow.com' || dnsDomainIs(host, '.gifshow.com') ||
        host === 'yximgs.com' || dnsDomainIs(host, '.yximgs.com') ||
        host === 'xiaomi.com' || dnsDomainIs(host, '.xiaomi.com') ||
        host === 'mi.com' || dnsDomainIs(host, '.mi.com') ||
        host === 'miui.com' || dnsDomainIs(host, '.miui.com') ||
        host === 'oppo.com' || dnsDomainIs(host, '.oppo.com') ||
        host === 'coloros.com' || dnsDomainIs(host, '.coloros.com') ||
        host === 'heytapmobi.com' || dnsDomainIs(host, '.heytapmobi.com') ||
        host === 'vivo.com' || dnsDomainIs(host, '.vivo.com') ||
        host === 'huawei.com' || dnsDomainIs(host, '.huawei.com') ||
        host === 'hicloud.com' || dnsDomainIs(host, '.hicloud.com') ||
        host === 'hihonor.com' || dnsDomainIs(host, '.hihonor.com') ||
        host === 'honor.cn' || dnsDomainIs(host, '.honor.cn') ||
        host === 'meituan.com' || dnsDomainIs(host, '.meituan.com') ||
        host === 'dianping.com' || dnsDomainIs(host, '.dianping.com') ||
        host === 'ctrip.com' || dnsDomainIs(host, '.ctrip.com') ||
        host === 'qunar.com' || dnsDomainIs(host, '.qunar.com') ||
        host === '12306.cn' || dnsDomainIs(host, '.12306.cn') ||
        host === 'netease.com' || dnsDomainIs(host, '.netease.com') ||
        host === '163.com' || dnsDomainIs(host, '.163.com') ||
        host === '126.net' || dnsDomainIs(host, '.126.net') ||
        host === 'music.163.com' || dnsDomainIs(host, '.music.163.com') ||
        host === 'sina.com.cn' || dnsDomainIs(host, '.sina.com.cn') ||
        host === 'weibo.com' || dnsDomainIs(host, '.weibo.com') ||
        host === 'zhihu.com' || dnsDomainIs(host, '.zhihu.com') ||
        host === 'xiaohongshu.com' || dnsDomainIs(host, '.xiaohongshu.com') ||
        host === 'xhscdn.com' || dnsDomainIs(host, '.xhscdn.com')) {
        return "DIRECT";
    }
    return "PROXY 127.0.0.1:7890; SOCKS5 127.0.0.1:7890";
}
