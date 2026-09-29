/**
 * M365 Copilot 历史对话一键批量清理脚本
 * 
 * 使用方法：
 * 1. 在 Edge 或 Chrome 中打开并登录 Microsoft 365 Copilot 页面（如 https://m365.cloud.microsoft 或 Edge 侧边栏 Copilot）
 * 2. 按 F12 打开开发者工具，切换到 Console (控制台) 标签页
 * 3. 将本脚本内容完整复制并粘贴到控制台中，按回车执行
 * 4. 脚本会自动分页扫描所有历史对话，在控制台输出表格核对，并在弹出确认框后自动逐一调用微软接口删除全部对话
 */

(async function() {
    console.log("🚀 开始获取 M365 对话列表...");
    const url = "https://m365.cloud.microsoft/resources/app-shell-action?FORM=undexpand&fromcode=edgesidebar&auth=2";
    
    let allChats = [];
    let syncState = null;
    let page = 1;

    // 1. 分页拉取所有对话历史（自动翻页拉取全部）
    while (true) {
        console.log(`正在拉取第 ${page} 页对话列表...`);
        const body = {
            action: "GetConversationPageHistoryList",
            enableLastMessage: true,
            conversationHistoryFilter: null,
            state: {
                conversationPageHistoryList: { chats: [] }
            }
        };
        if (syncState) {
            body.syncState = syncState;
        }

        const res = await fetch(url, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "Accept": "application/json, text/plain, */*",
                "X-Requested-With": "XMLHttpRequest"
            },
            body: JSON.stringify(body)
        });

        if (!res.ok) {
            console.error(`拉取第 ${page} 页失败: HTTP ${res.status}`);
            break;
        }

        const data = await res.json();
        const hist = data?.store?.conversationPageHistoryList || data?.state?.conversationPageHistoryList;
        const chats = hist?.chats || [];
        if (chats.length === 0) {
            break;
        }

        allChats.push(...chats);
        console.log(`第 ${page} 页获取到 ${chats.length} 条对话，累计找到: ${allChats.length} 条`);

        const nextSync = hist?.syncState;
        if (!nextSync || nextSync === syncState) {
            break;
        }
        syncState = nextSync;
        page++;
        await new Promise(r => setTimeout(r, 400));
    }

    console.log(`🎉 扫描完成，共找到 ${allChats.length} 个历史对话！`);
    if (allChats.length === 0) {
        console.log("当前没有任何对话记录需要清理。");
        return;
    }

    // 表格打印供核对
    console.table(allChats.map(c => ({
        ID: c.conversationId,
        标题: c.chatName,
        模型: c.tone,
        创建时间: new Date(c.createTimeUtc).toLocaleString()
    })));

    // 弹窗确认，避免误删
    if (!confirm(`共扫描到 ${allChats.length} 个历史对话记录，确定要全部删除吗？`)) {
        console.log("用户取消操作。");
        return;
    }

    // 2. 并发删除（5并发）
    let success = 0;
    let fail = 0;
    let completed = 0;
    const CONCURRENCY = 5;

    async function deleteConversation(c) {
        try {
            const delRes = await fetch(url, {
                method: "POST",
                headers: {
                    "Content-Type": "application/json",
                    "Accept": "application/json, text/plain, */*",
                    "X-Requested-With": "XMLHttpRequest"
                },
                body: JSON.stringify({
                    action: "DeleteConversation",
                    conversationId: c.conversationId,
                    state: {
                        conversationPageHistoryList: { chats: [] },
                        taskConversationPageHistoryList: { chats: [] }
                    }
                })
            });

            const delData = await delRes.json();
            completed++;

            if (delData.error) {
                fail++;
                console.warn(`[${completed}/${allChats.length}] ❌ 删除失败: ${c.chatName}`);
            } else {
                success++;
                console.log(`[${completed}/${allChats.length}] ✅ 已删除: ${c.chatName}`);
            }
        } catch (e) {
            completed++;
            fail++;
            console.error(`[${completed}/${allChats.length}] ❌ 异常: ${e.message}`);
        }
    }

    let index = 0;
    const workers = Array.from({ length: Math.min(CONCURRENCY, allChats.length) }, async () => {
        while (index < allChats.length) {
            const current = allChats[index++];
            await deleteConversation(current);
            await new Promise(r => setTimeout(r, 50));
        }
    });

    await Promise.all(workers);

    console.log(`\n🏁 全部清理完成！成功删除: ${success} 条，失败: ${fail} 条。刷新页面即可查看清空后的效果。`);
})();
